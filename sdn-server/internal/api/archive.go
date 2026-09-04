package api

// $DPM archive lane (fbcs program): a selection of stored records becomes a
// signed, immutable archive (shard + index + $DPM manifest on the archive
// plane, pinned when the node has Kubo), every archive this node holds lists
// as its $DPM, its assets are served locally when /ipfs is unavailable, and
// an archive re-imports with its ORIGINAL producer kept.
//
//	POST /api/v1/archive                       body = one $QRP{KIND=Request}   admin
//	GET  /api/v1/archives[?schema=]            $DPM frames, newest first       anonymous read
//	GET  /api/v1/archives/{manifestCID}/asset/{assetCID}                       anonymous read
//	POST /api/v1/archive/import                body = one $QRP{CID|ARCHIVE_ID}  admin

import (
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	dpm "github.com/DigitalArsenal/spacedatastandards.org/lib/go/DPM"
	standardsEPM "github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/QRP"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func init() {
	RegisterAdminMount("archive", mountArchive)
}

const (
	// ArchivePath creates an archive.
	ArchivePath = "/api/v1/archive"
	// ArchiveImportPath re-imports one.
	ArchiveImportPath = "/api/v1/archive/import"
	// ArchivesPath lists archives and serves their assets.
	ArchivesPath = "/api/v1/archives"
	// ArchiveSchemaName is the store form of the $DPM standard.
	ArchiveSchemaName = "DPM.fbs"

	// archiveSelectionCap is the most records one archive selection holds
	// (the indexed query's large-result page).
	archiveSelectionCap = 250000
	// archiveRequestFrameBytes bounds an archive request body.
	archiveRequestFrameBytes int64 = 1 << 20
	// archiveImportTimeout bounds one import.
	archiveImportTimeout = 2 * time.Hour
)

// ArchiveHandler serves the archive lane.
type ArchiveHandler struct {
	deps *AdminMountDeps
	sync *SyncHandler
}

// NewArchiveHandler builds a handler over the mount deps; imports run on the
// given sync handler's lane actions.
func NewArchiveHandler(deps *AdminMountDeps, syncHandler *SyncHandler) *ArchiveHandler {
	if deps == nil {
		deps = &AdminMountDeps{}
	}
	if syncHandler == nil {
		syncHandler = syncHandlerFor(deps)
	}
	return &ArchiveHandler{deps: deps, sync: syncHandler}
}

func mountArchive(mux *http.ServeMux, deps *AdminMountDeps) {
	h := NewArchiveHandler(deps, syncHandlerFor(deps))
	h.RegisterRoutes(mux)
	log.Infof("Archive lane ($DPM) mounted at %s", ArchivePath)
}

// RegisterRoutes mounts the archive routes.
func (h *ArchiveHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc(ArchivePath, h.deps.adminGate(h.handleCreate))
	mux.HandleFunc(ArchiveImportPath, h.deps.adminGate(h.handleImport))
	mux.HandleFunc(ArchivesPath, h.handleList)
	mux.HandleFunc(ArchivesPath+"/", h.handleAsset)
}

// sizePrefixFrame wraps a bare finished buffer as one size-prefixed frame; an
// already prefixed $DPM is returned unchanged.
func sizePrefixFrame(bare []byte) []byte {
	if len(bare) >= frameIdentifierOffset+frameIdentifierLength && dpm.SizePrefixedDPMBufferHasIdentifier(bare) {
		return bare
	}
	out := make([]byte, framePrefixLength+len(bare))
	binary.LittleEndian.PutUint32(out[:framePrefixLength], uint32(len(bare)))
	copy(out[framePrefixLength:], bare)
	return out
}

// DecodeDPM reads a $DPM from a bare or size-prefixed buffer.
func DecodeDPM(data []byte) (*dpm.DPM, error) {
	bare := storage.BareDPMBytes(data)
	if len(bare) < framePrefixLength+frameIdentifierLength || !dpm.DPMBufferHasIdentifier(bare) {
		return nil, fmt.Errorf("buffer is not a $DPM")
	}
	return dpm.GetRootAsDPM(bare, 0), nil
}

// --- create -----------------------------------------------------------------

func (h *ArchiveHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteErrorFrame(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use POST with one $QRP request frame to create an archive.", 0)
		return
	}
	store := h.deps.Store
	if store == nil {
		WriteErrorFrame(w, http.StatusServiceUnavailable, "unavailable", "The record store is not available on this node.", 5*time.Second)
		return
	}
	req, err := readQRPRequest(r, archiveRequestFrameBytes)
	if err != nil {
		WriteErrorFrame(w, http.StatusBadRequest, "bad_request", "The request body must be exactly one $QRP frame.", 0)
		return
	}
	schema := storeSchemaName(string(req.SchemaName()))
	if schema == "" {
		WriteErrorFrame(w, http.StatusBadRequest, "bad_request", "The archive request must name a schema.", 0)
		return
	}
	if len(h.deps.SigningKey) != ed25519.PrivateKeySize {
		WriteErrorFrame(w, http.StatusServiceUnavailable, "no_signing_key", "This node has no publication signing key.", 0)
		return
	}
	if strings.TrimSpace(h.deps.NodePeerID) == "" {
		WriteErrorFrame(w, http.StatusServiceUnavailable, "no_identity", "This node has no peer identity to sign an archive with.", 0)
		return
	}
	if !store.RecordCatalogHydrated() {
		WriteErrorFrame(w, http.StatusServiceUnavailable, "hydrating", "The record catalog is still loading; an archive would be incomplete. Try again shortly.", 30*time.Second)
		return
	}

	code := strings.TrimSuffix(schema, ".fbs")
	filter := storage.IndexedRecordQuery{
		SchemaName:          schema,
		ProviderID:          strings.TrimSpace(string(req.ProviderId())),
		SourceName:          strings.TrimSpace(string(req.SourceName())),
		BatchID:             strings.TrimSpace(string(req.BatchId())),
		Limit:               archiveSelectionCap,
		AllowLargeResultSet: true,
	}
	if ms := req.FromEpochMs(); ms > 0 {
		t := time.UnixMilli(ms).UTC()
		filter.From = &t
	}
	if ms := req.ToEpochMs(); ms > 0 {
		t := time.UnixMilli(ms).UTC()
		filter.To = &t
	}
	if limit := int(req.LIMIT()); limit > 0 && limit < archiveSelectionCap {
		filter.Limit = limit
	}

	// A selection larger than one archive holds is refused up front when the
	// summary can count it; an archive is permanent once written.
	if filter.From == nil && filter.To == nil && int(req.LIMIT()) == 0 {
		if summary, err := store.DataSummary(); err == nil {
			total := archiveSelectionCount(summary, filter)
			if total == 0 {
				WriteErrorFrame(w, http.StatusBadRequest, "empty_selection", "No records match that selection.", 0)
				return
			}
			if total > archiveSelectionCap {
				WriteErrorFrame(w, http.StatusRequestEntityTooLarge, "too_large",
					fmt.Sprintf("That selection holds %d records; one archive holds at most %d. Narrow it by batch or time window.", total, archiveSelectionCap), 0)
				return
			}
		}
	}

	archiveID := strings.TrimSpace(string(req.ArchiveId()))
	now := time.Now().UTC()
	if archiveID == "" {
		archiveID = fmt.Sprintf("archive-%s-%s", strings.ToLower(code), now.Format("20060102T150405Z"))
	}
	if strings.ContainsAny(archiveID, "/\\") {
		WriteErrorFrame(w, http.StatusBadRequest, "bad_request", "The archive id must not contain path separators.", 0)
		return
	}

	opts := storage.ArchiveDatasetOptions{
		ArchiveID:       archiveID,
		ProviderPeerID:  h.deps.NodePeerID,
		ProviderEPMCID:  h.deps.NodeEPMCID,
		SigningKey:      h.deps.SigningKey,
		PublishedAt:     now,
		IPFSAPIURL:      strings.TrimSpace(h.deps.IPFSAPIURL),
		SourceFeedHeads: h.sourceFeedHeads(store, filter),
	}
	ctx, cancel := context.WithTimeout(r.Context(), archiveImportTimeout)
	defer cancel()
	archive, err := store.ArchiveDatasetSelection(ctx, filter, opts)
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrRecordCatalogHydrating):
			WriteErrorFrame(w, http.StatusServiceUnavailable, "hydrating", "The record catalog is still loading; an archive would be incomplete. Try again shortly.", 30*time.Second)
		case strings.Contains(err.Error(), "no records match"):
			WriteErrorFrame(w, http.StatusBadRequest, "empty_selection", "No records match that selection.", 0)
		default:
			log.Warnf("Archive %s: %v", archiveID, err)
			WriteErrorFrame(w, http.StatusInternalServerError, "archive_failed", "The archive could not be written. Try again.", 0)
		}
		return
	}
	// Lane-scoped ledger rows so the lane's $DSS reports PIN_POLICY Archive.
	if filter.ProviderID != "" || filter.SourceName != "" {
		for _, entry := range archive.PinEntries {
			entry.ProviderID, entry.SourceName = filter.ProviderID, filter.SourceName
			if err := store.UpsertPinLedgerEntry(entry); err != nil {
				log.Warnf("Archive %s: lane ledger row %s: %v", archiveID, entry.CID, err)
			}
		}
	}
	WriteFrameStream(w, http.StatusAccepted, [][]byte{sizePrefixFrame(archive.Manifest.Bytes)}, map[string]string{
		StreamSchemaHeader: ArchiveSchemaName,
		"Cache-Control":    "no-store",
	})
}

// readQRPRequest reads exactly one $QRP frame from a request body.
func readQRPRequest(r *http.Request, max int64) (*QRP.QRP, error) {
	frames, err := ReadFrames(r.Body, max)
	if err != nil {
		return nil, err
	}
	if len(frames) != 1 || FrameIdentifier(frames[0]) != "$QRP" {
		return nil, errors.New("body must be exactly one $QRP frame")
	}
	return ParseQRP(frames[0])
}

// archiveSelectionCount counts the summary rows a provider/source/batch
// selection covers.
func archiveSelectionCount(summary *storage.DataSummary, filter storage.IndexedRecordQuery) int64 {
	if summary == nil {
		return 0
	}
	var total int64
	for _, src := range summary.Sources {
		if src.SchemaName != filter.SchemaName {
			continue
		}
		if filter.ProviderID != "" && src.ProviderID != filter.ProviderID {
			continue
		}
		if filter.SourceName != "" && src.SourceName != filter.SourceName {
			continue
		}
		if filter.BatchID != "" && src.BatchID != filter.BatchID {
			continue
		}
		total += src.Count
	}
	return total
}

// sourceFeedHeads decides the manifest's feed-head provenance: derived (nil)
// when every (provider, source, batch) lane in the selection has a
// publication to point at, else an explicit empty list — an archive of
// records that were never published carries no feed head rather than failing.
func (h *ArchiveHandler) sourceFeedHeads(store *storage.FlatSQLStore, filter storage.IndexedRecordQuery) []storage.ArchiveSourceFeedHead {
	empty := []storage.ArchiveSourceFeedHead{}
	heads, err := store.ArchiveSourceFeedHeads(filter)
	if err != nil || len(heads) == 0 {
		return empty
	}
	summary, err := store.DataSummary()
	if err != nil {
		return empty
	}
	type lane struct{ provider, source, batch string }
	covered := map[lane]bool{}
	for _, head := range heads {
		if strings.TrimSpace(head.ManifestCID) == "" {
			return empty
		}
		covered[lane{head.ProviderID, head.SourceName, head.BatchID}] = true
	}
	for _, src := range summary.Sources {
		if src.SchemaName != filter.SchemaName || src.Count <= 0 {
			continue
		}
		if filter.ProviderID != "" && src.ProviderID != filter.ProviderID {
			continue
		}
		if filter.SourceName != "" && src.SourceName != filter.SourceName {
			continue
		}
		if filter.BatchID != "" && src.BatchID != filter.BatchID {
			continue
		}
		if !covered[lane{src.ProviderID, src.SourceName, src.BatchID}] {
			return empty
		}
	}
	return nil
}

// --- list + assets ----------------------------------------------------------

// heldArchive is one archive the ledger knows and the plane still holds.
type heldArchive struct {
	archiveID   string
	manifestCID string
	schema      string
	path        string
	bytes       []byte
	updatedAt   time.Time
}

// heldArchives lists the archives on the plane, newest first.
func (h *ArchiveHandler) heldArchives(schema string) ([]heldArchive, error) {
	store := h.deps.Store
	if store == nil {
		return nil, errors.New("archive lane needs a store")
	}
	entries, err := store.ListPinLedgerEntries(storage.PinLedgerQuery{Role: storage.PinLedgerRoleArchive, SchemaName: storeSchemaName(schema)})
	if err != nil {
		return nil, err
	}
	type groupKey struct{ archiveID, manifestCID string }
	groups := map[groupKey]*heldArchive{}
	order := make([]groupKey, 0)
	manifestHash := map[groupKey]string{}
	for _, entry := range entries {
		key := groupKey{strings.TrimSpace(entry.BatchID), strings.TrimSpace(entry.SnapshotID)}
		if key.archiveID == "" || key.manifestCID == "" {
			continue
		}
		g := groups[key]
		if g == nil {
			g = &heldArchive{archiveID: key.archiveID, manifestCID: key.manifestCID, schema: entry.SchemaName}
			groups[key] = g
			order = append(order, key)
		}
		stamp := entry.UpdatedAt
		if entry.VerifiedAt.After(stamp) {
			stamp = entry.VerifiedAt
		}
		if stamp.After(g.updatedAt) {
			g.updatedAt = stamp
		}
		if entry.CID == key.manifestCID && len(entry.ByteHash) >= 16 {
			manifestHash[key] = entry.ByteHash
		}
	}
	manifestsDir := filepath.Join(store.ArchiveOutputDir(), "manifests")
	held := make([]heldArchive, 0, len(order))
	for _, key := range order {
		g := groups[key]
		var candidates []string
		if hash, ok := manifestHash[key]; ok {
			candidates = append(candidates, filepath.Join(manifestsDir, fmt.Sprintf("%s-%s.dpm", key.archiveID, hash[:16])))
		}
		if matches, err := filepath.Glob(filepath.Join(manifestsDir, key.archiveID+"-*.dpm")); err == nil {
			candidates = append(candidates, matches...)
		}
		for _, path := range candidates {
			data, err := os.ReadFile(path)
			if err != nil || storage.ComputeCID(data) != key.manifestCID {
				continue
			}
			g.path, g.bytes = path, data
			break
		}
		if g.bytes == nil {
			continue // the ledger remembers it but the plane no longer holds it
		}
		held = append(held, *g)
	}
	sort.SliceStable(held, func(i, j int) bool { return held[i].updatedAt.After(held[j].updatedAt) })
	return held, nil
}

func (h *ArchiveHandler) handleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteErrorFrame(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use GET to list archives.", 0)
		return
	}
	held, err := h.heldArchives(r.URL.Query().Get("schema"))
	if err != nil {
		WriteErrorFrame(w, http.StatusServiceUnavailable, "unavailable", "Archives are not available right now.", 5*time.Second)
		return
	}
	frames := make([][]byte, 0, len(held))
	for i := range held {
		frames = append(frames, sizePrefixFrame(held[i].bytes))
	}
	WriteFrameStream(w, http.StatusOK, frames, map[string]string{StreamSchemaHeader: ArchiveSchemaName})
}

// handleAsset serves GET /api/v1/archives/{manifestCID}/asset/{assetCID}.
func (h *ArchiveHandler) handleAsset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteErrorFrame(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use GET to read an archive asset.", 0)
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, ArchivesPath+"/"), "/")
	parts := strings.Split(rest, "/")
	if len(parts) != 3 || parts[1] != "asset" {
		WriteErrorFrame(w, http.StatusNotFound, "not_found", "An archive asset is addressed as /api/v1/archives/{manifestCID}/asset/{assetCID}.", 0)
		return
	}
	manifestCID, _ := url.PathUnescape(parts[0])
	assetCID, _ := url.PathUnescape(parts[2])
	manifestCID, assetCID = strings.TrimSpace(manifestCID), strings.TrimSpace(assetCID)
	archive, ok := h.heldArchiveByCID(manifestCID)
	if !ok {
		WriteErrorFrame(w, http.StatusNotFound, "not_found", "That archive is not held on this node.", 0)
		return
	}
	if assetCID == manifestCID {
		serveArchiveBytes(w, filepath.Base(archive.path), archive.bytes)
		return
	}
	manifest, err := DecodeDPM(archive.bytes)
	if err != nil {
		WriteErrorFrame(w, http.StatusInternalServerError, "archive_failed", "The archive manifest could not be read.", 0)
		return
	}
	plane := h.deps.Store.ArchiveOutputDir()
	var asset dpm.DPMAsset
	for i := 0; i < manifest.ASSETSLength(); i++ {
		if !manifest.ASSETS(&asset, i) || strings.TrimSpace(string(asset.CID())) != assetCID {
			continue
		}
		fileName := strings.TrimSpace(string(asset.FILE_NAME()))
		if fileName == "" || fileName != filepath.Base(fileName) || fileName == "." || fileName == ".." {
			break
		}
		var subdir string
		switch asset.ASSET_KIND().String() {
		case "DATA_SHARD":
			subdir = "shards"
		case "QUERY_INDEX":
			subdir = "indexes"
		default:
			WriteErrorFrame(w, http.StatusNotFound, "not_found", "That asset is a reference, not a file this node holds.", 0)
			return
		}
		path := filepath.Join(plane, subdir, fileName)
		data, err := os.ReadFile(path)
		if err != nil {
			WriteErrorFrame(w, http.StatusNotFound, "not_found", "That asset is no longer held on this node.", 0)
			return
		}
		if want := strings.TrimSpace(string(asset.BYTE_SHA256())); want != "" && sha256Hex(data) != want {
			WriteErrorFrame(w, http.StatusConflict, "hash_mismatch", "The held asset does not match the archive manifest.", 0)
			return
		}
		if storage.ComputeCID(data) != assetCID {
			WriteErrorFrame(w, http.StatusConflict, "hash_mismatch", "The held asset does not match the archive manifest.", 0)
			return
		}
		serveArchiveBytes(w, fileName, data)
		return
	}
	WriteErrorFrame(w, http.StatusNotFound, "not_found", "That archive has no asset with that CID.", 0)
}

func serveArchiveBytes(w http.ResponseWriter, fileName string, data []byte) {
	hdr := w.Header()
	hdr.Set("Content-Type", "application/octet-stream")
	hdr.Set("Content-Disposition", `attachment; filename="`+exportFileNameSanitizer.ReplaceAllString(fileName, "-")+`"`)
	hdr.Set("Cache-Control", "public, max-age=31536000, immutable")
	hdr.Set("Content-Length", fmt.Sprint(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (h *ArchiveHandler) heldArchiveByCID(manifestCID string) (heldArchive, bool) {
	if manifestCID == "" {
		return heldArchive{}, false
	}
	held, err := h.heldArchives("")
	if err != nil {
		return heldArchive{}, false
	}
	for i := range held {
		if held[i].manifestCID == manifestCID {
			return held[i], true
		}
	}
	// The ledger may not know it yet (a manifest copied onto the plane by
	// hand): fall back to the plane itself.
	for _, candidate := range h.planeManifests() {
		if candidate.manifestCID == manifestCID {
			return candidate, true
		}
	}
	return heldArchive{}, false
}

// planeManifests reads every manifest file on the archive plane, newest
// file name last.
func (h *ArchiveHandler) planeManifests() []heldArchive {
	if h.deps.Store == nil {
		return nil
	}
	matches, _ := filepath.Glob(filepath.Join(h.deps.Store.ArchiveOutputDir(), "manifests", "*.dpm"))
	sort.Strings(matches)
	out := make([]heldArchive, 0, len(matches))
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		manifest, err := DecodeDPM(data)
		if err != nil {
			continue
		}
		out = append(out, heldArchive{
			archiveID:   strings.TrimSpace(string(manifest.DATASET_ID())),
			manifestCID: storage.ComputeCID(storage.BareDPMBytes(data)),
			path:        path,
			bytes:       storage.BareDPMBytes(data),
		})
	}
	return out
}

func (h *ArchiveHandler) heldArchiveByID(archiveID string) (heldArchive, bool) {
	if archiveID == "" {
		return heldArchive{}, false
	}
	held, err := h.heldArchives("")
	if err != nil {
		return heldArchive{}, false
	}
	for i := range held {
		if held[i].archiveID == archiveID {
			return held[i], true
		}
	}
	// The ledger may not know it yet (a manifest copied onto the plane by
	// hand): fall back to the plane itself, newest file last.
	candidates := h.planeManifests()
	for i := len(candidates) - 1; i >= 0; i-- {
		if candidates[i].archiveID == archiveID {
			return candidates[i], true
		}
	}
	return heldArchive{}, false
}

// --- import -----------------------------------------------------------------

func (h *ArchiveHandler) handleImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteErrorFrame(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use POST with one $QRP request frame to import an archive.", 0)
		return
	}
	store := h.deps.Store
	if store == nil {
		WriteErrorFrame(w, http.StatusServiceUnavailable, "unavailable", "The record store is not available on this node.", 5*time.Second)
		return
	}
	req, err := readQRPRequest(r, archiveRequestFrameBytes)
	if err != nil {
		WriteErrorFrame(w, http.StatusBadRequest, "bad_request", "The request body must be exactly one $QRP frame.", 0)
		return
	}
	manifestCID := strings.TrimSpace(string(req.CID()))
	archiveID := strings.TrimSpace(string(req.ArchiveId()))
	if manifestCID == "" && archiveID == "" {
		WriteErrorFrame(w, http.StatusBadRequest, "bad_request", "The import request must carry the archive's manifest CID or its archive id.", 0)
		return
	}

	// Locate the manifest: the plane first, then Kubo.
	var manifestBytes []byte
	switch {
	case manifestCID != "":
		if held, ok := h.heldArchiveByCID(manifestCID); ok {
			manifestBytes = held.bytes
		} else if apiURL := strings.TrimSpace(h.deps.IPFSAPIURL); apiURL != "" {
			ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
			data, err := storage.FetchIPFSBlockByCID(ctx, apiURL, manifestCID)
			cancel()
			if err == nil && storage.ComputeCID(storage.BareDPMBytes(data)) == manifestCID {
				manifestBytes = storage.BareDPMBytes(data)
			}
		}
	default:
		if held, ok := h.heldArchiveByID(archiveID); ok {
			manifestBytes = held.bytes
		}
	}
	if len(manifestBytes) == 0 {
		WriteErrorFrame(w, http.StatusNotFound, "not_found", "That archive is not available on this node.", 0)
		return
	}
	manifest, err := DecodeDPM(manifestBytes)
	if err != nil {
		WriteErrorFrame(w, http.StatusBadRequest, "bad_request", "The archive manifest could not be read.", 0)
		return
	}
	providerPeerID := strings.TrimSpace(string(manifest.PROVIDER_PEER_ID()))
	providerKey, ok := h.providerPublicKey(providerPeerID, manifestBytes)
	if !ok {
		WriteErrorFrame(w, http.StatusForbidden, "unverified", "The archive's provider signature could not be verified on this node.", 0)
		return
	}

	schema, providerID, sourceName := archiveLane(manifest)
	if schema == "" {
		WriteErrorFrame(w, http.StatusBadRequest, "bad_request", "The archive manifest names no schema.", 0)
		return
	}
	finish, ok := h.sync.StartLaneAction(schema, providerID, sourceName, "import")
	if !ok {
		WriteErrorFrame(w, http.StatusConflict, "busy", "An action is already running on that source.", 5*time.Second)
		return
	}
	var fetch func(context.Context, string) ([]byte, error)
	if apiURL := strings.TrimSpace(h.deps.IPFSAPIURL); apiURL != "" {
		fetch = func(ctx context.Context, cid string) ([]byte, error) {
			return storage.FetchIPFSBlockByCID(ctx, apiURL, cid)
		}
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), archiveImportTimeout)
		defer cancel()
		result, err := storage.ImportArchiveFromManifest(ctx, store, storage.ImportArchiveOptions{
			ManifestBytes:     manifestBytes,
			ProviderPublicKey: providerKey,
			Fetch:             fetch,
		})
		if err != nil {
			finish(0, err)
			return
		}
		finish(result.Imported, nil)
	}()
	h.sync.writeLanes(w, SyncFilter{Schema: schema, ProviderID: providerID, SourceName: sourceName, Exact: true}, http.StatusAccepted)
}

// archiveLane reads the (schema, provider, source) lane an archive lands on.
func archiveLane(manifest *dpm.DPM) (schema, providerID, sourceName string) {
	var asset dpm.DPMAsset
	for i := 0; i < manifest.ASSETSLength(); i++ {
		if manifest.ASSETS(&asset, i) && asset.ASSET_KIND().String() == "DATA_SHARD" {
			schema = storeSchemaName(string(asset.SCHEMA_NAME()))
			break
		}
	}
	if query := manifest.QUERY(nil); query != nil {
		if schema == "" && query.SCHEMA_NAMESLength() > 0 {
			schema = storeSchemaName(string(query.SCHEMA_NAMES(0)))
		}
		if query.PROVIDER_IDSLength() > 0 {
			providerID = strings.TrimSpace(string(query.PROVIDER_IDS(0)))
		}
		if query.SOURCE_NAMESLength() > 0 {
			sourceName = strings.TrimSpace(string(query.SOURCE_NAMES(0)))
		}
	}
	return schema, providerID, sourceName
}

// providerPublicKey resolves the Ed25519 key that signed an archive: this
// node's own publication key for its own archives, else the peer identity key
// or a signing key advertised in the provider's verified $EPM — whichever
// actually verifies the manifest.
func (h *ArchiveHandler) providerPublicKey(providerPeerID string, manifestBytes []byte) (ed25519.PublicKey, bool) {
	var candidates []ed25519.PublicKey
	if providerPeerID != "" && providerPeerID == strings.TrimSpace(h.deps.NodePeerID) && len(h.deps.SigningKey) == ed25519.PrivateKeySize {
		if pub, ok := h.deps.SigningKey.Public().(ed25519.PublicKey); ok {
			candidates = append(candidates, pub)
		}
	}
	if providerPeerID != "" {
		if id, err := peer.Decode(providerPeerID); err == nil {
			if pubKey, err := id.ExtractPublicKey(); err == nil {
				if raw, err := pubKey.Raw(); err == nil && len(raw) == ed25519.PublicKeySize {
					candidates = append(candidates, ed25519.PublicKey(raw))
				}
			}
		}
		if h.deps.Store != nil {
			if records, err := h.deps.Store.QueryDirectory(storage.DirectoryQuery{PeerID: providerPeerID, Limit: 1}); err == nil && len(records) > 0 {
				if record, ok := verifiedPublisherEPM(records[0], providerPeerID); ok {
					candidates = append(candidates, epmSigningKeys(record)...)
				}
			}
		}
	}
	for _, key := range candidates {
		if _, err := storage.VerifySignedDatasetPublicationManifest(storage.BareDPMBytes(manifestBytes), key); err == nil {
			return key, true
		}
	}
	return nil, false
}

// epmSigningKeys lists the Ed25519 signing keys a verified $EPM advertises.
func epmSigningKeys(record *standardsEPM.EPM) []ed25519.PublicKey {
	var keys []ed25519.PublicKey
	key := new(standardsEPM.CryptoKey)
	for i := 0; i < record.KEYSLength(); i++ {
		if !record.KEYS(key, i) || key.KEY_TYPE() != standardsEPM.KeyTypeSigning {
			continue
		}
		algorithm := strings.ToLower(strings.TrimSpace(string(key.ALGORITHM())))
		if algorithm == "" {
			algorithm = strings.ToLower(strings.TrimSpace(string(key.ADDRESS_TYPE())))
		}
		if algorithm != "" && algorithm != "ed25519" {
			continue
		}
		raw, err := hex.DecodeString(strings.TrimSpace(string(key.PUBLIC_KEY())))
		if err != nil || len(raw) != ed25519.PublicKeySize {
			continue
		}
		keys = append(keys, ed25519.PublicKey(raw))
	}
	return keys
}

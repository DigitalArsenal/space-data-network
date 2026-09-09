package channels

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/DPM"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// Catalogs retain canonical SDS buffers independently of append-only dataset
// streams. One atomically replaced ZIP (stored, never compressed) per publisher
// and source contains the original DPM, PNM and public verification key. This
// local container keeps updates physically bounded; it is not a wire format.
const MaxDatasetCatalogManifestBytes = 8 << 20

const maxDatasetCatalogBytes = 128 << 20
const maxDatasetCatalogEntries = 512
const maxDatasetCatalogPeerEntries = 64

type DatasetCatalogEntry struct {
	SchemaName, ProviderID, SourceName     string
	PeerID, PublicKey, ManifestCID, PNMCID string
	PublishedAt                            time.Time
	Rows                                   uint64
	fileID                                 string
}

func (e DatasetCatalogEntry) sourceKey() string {
	sum := sha256.Sum256([]byte(e.SchemaName + "\x00" + e.ProviderID + "\x00" + e.SourceName))
	return hex.EncodeToString(sum[:])
}

// DecodeDatasetCatalog verifies the DPM before interpreting its discovery fields.
// It accepts a single schema/provider/source scope; multi-source query results
// cannot honestly describe one selectable dataset and are not catalog entries.
func DecodeDatasetCatalog(raw []byte, publicKey ed25519.PublicKey, publisher string, now time.Time) (entry DatasetCatalogEntry, err error) {
	defer func() {
		if recover() != nil {
			entry = DatasetCatalogEntry{}
			err = errors.New("malformed dataset manifest")
		}
	}()
	if len(raw) < 12 || len(raw) > MaxDatasetCatalogManifestBytes {
		return entry, errors.New("dataset manifest exceeds catalog bounds")
	}
	evidence, err := storage.VerifySignedDatasetPublicationManifest(raw, publicKey)
	if err != nil {
		return entry, err
	}
	if publisher == "" {
		publisher = evidence.ProviderPeer
	}
	if evidence.ProviderPeer != publisher || publisher == "" || evidence.Encrypted {
		return entry, errors.New("public dataset manifest publisher mismatch or encrypted content")
	}
	manifest := DPM.GetRootAsDPM(raw, 0)
	query := manifest.QUERY(nil)
	if query == nil || query.SCHEMA_NAMESLength() != 1 || query.PROVIDER_IDSLength() != 1 || query.SOURCE_NAMESLength() != 1 {
		return entry, errors.New("dataset catalog requires an exact source scope")
	}
	entry = DatasetCatalogEntry{SchemaName: string(query.SCHEMA_NAMES(0)), ProviderID: string(query.PROVIDER_IDS(0)), SourceName: string(query.SOURCE_NAMES(0)), PeerID: publisher, PublicKey: hex.EncodeToString(publicKey), ManifestCID: evidence.ManifestCID}
	entry.fileID = evidence.FileID
	fileSchema := ""
	for _, part := range strings.Split(entry.fileID, ":") {
		if strings.HasSuffix(part, ".fbs") {
			fileSchema = part
			break
		}
	}
	if fileSchema != entry.SchemaName {
		return DatasetCatalogEntry{}, errors.New("dataset manifest file identity differs from query schema")
	}
	// A filtered page is not a replacement for a provider's source catalog.
	var scope storage.IndexedRecordQuery
	if err := json.Unmarshal(query.CANONICAL_QUERY(), &scope); err != nil || scope.SchemaName != entry.SchemaName || scope.ProviderID != entry.ProviderID || scope.SourceName != entry.SourceName || scope.Offset != 0 || scope.Day != "" || scope.NoradCatID != nil || scope.EntityID != "" || scope.ObjectType != "" || scope.OpsStatusCode != "" || scope.ActivePayloads || scope.CAReadyResidentSet || scope.From != nil || scope.To != nil {
		return DatasetCatalogEntry{}, errors.New("dataset catalog requires an unfiltered source publication")
	}
	if _, err := StandardCodeFromSchemaName(entry.SchemaName); err != nil {
		return DatasetCatalogEntry{}, err
	}
	for _, value := range []string{entry.ProviderID, entry.SourceName} {
		if len(value) == 0 || len(value) > 512 || strings.ContainsRune(value, 0) {
			return DatasetCatalogEntry{}, errors.New("invalid dataset catalog source")
		}
	}
	entry.PublishedAt, err = time.Parse(time.RFC3339Nano, string(manifest.PUBLISH_TIMESTAMP()))
	if err != nil || entry.PublishedAt.After(now.Add(5*time.Minute)) {
		return DatasetCatalogEntry{}, errors.New("invalid dataset catalog publication time")
	}
	if manifest.SOURCESLength() > 100000 {
		return DatasetCatalogEntry{}, errors.New("dataset source count exceeds catalog bounds")
	}
	for i := 0; i < manifest.SOURCESLength(); i++ {
		var source DPM.DPMSourceBatch
		if !manifest.SOURCES(&source, i) || string(source.SOURCE_NAME()) != entry.SourceName {
			return DatasetCatalogEntry{}, errors.New("dataset source differs from signed query")
		}
		count := source.RECORD_COUNT()
		if count > uint64(1<<53)-entry.Rows {
			return DatasetCatalogEntry{}, errors.New("dataset count exceeds exact client integer range")
		}
		entry.Rows += count
	}
	return entry, nil
}

func (e DatasetCatalogEntry) cacheFileName() string {
	peerHash := sha256.Sum256([]byte(e.PeerID))
	return hex.EncodeToString(peerHash[:]) + "-" + e.sourceKey() + ".zip"
}

func datasetCatalogPath(store *storage.FlatSQLStore) string {
	return filepath.Join(filepath.Dir(store.Path()), "dataset-catalogs")
}

// RememberDatasetCatalog is called serially by the node's announcement handler.
// The signed publication time prevents rollback. Replacement overwrites the old
// archive on disk instead of accumulating retired bytes in FlatSQL streams.
func RememberDatasetCatalog(store *storage.FlatSQLStore, publisher string, key ed25519.PublicKey, pnm, manifest []byte, now time.Time) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("malformed dataset announcement")
		}
	}()
	if store == nil || len(pnm) < 12 || len(pnm) > 65536 {
		return errors.New("dataset announcement exceeds catalog bounds")
	}
	if store.IsReadOnly() {
		return errors.New("dataset catalog store is read-only")
	}
	entry, err := DecodeDatasetCatalog(manifest, key, publisher, now)
	if err != nil {
		return err
	}
	proof, err := VerifySignedPNMEnvelopeWithProviderKey(pnm, key)
	if err != nil {
		return err
	}
	if proof.CID != entry.ManifestCID || proof.FileID != entry.fileID {
		return errors.New("dataset announcement differs from manifest")
	}
	dir := datasetCatalogPath(store)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	path := filepath.Join(dir, entry.cacheFileName())
	if old, err := readDatasetCatalogArchive(path, now); err == nil {
		if !entry.PublishedAt.After(old.PublishedAt) {
			return nil
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read existing dataset catalog: %w", err)
	}
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, file := range []struct {
		name string
		data []byte
	}{{"manifest.dpm", manifest}, {"announcement.pnm", pnm}, {"public-key.ed25519", key}} {
		out, err := writer.CreateHeader(&zip.FileHeader{Name: file.name, Method: zip.Store})
		if err != nil {
			return err
		}
		if _, err := out.Write(file.data); err != nil {
			return err
		}
	}
	if err := writer.Close(); err != nil {
		return err
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var diskBytes, previousBytes int64
	var total, peerTotal int
	for _, file := range files {
		info, err := file.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("unexpected non-regular catalog cache entry")
		}
		// Only this serialized writer creates these temporary names. A
		// process interruption may leave one; it has never been admitted.
		if strings.HasPrefix(file.Name(), ".pending-") {
			if err := os.Remove(filepath.Join(dir, file.Name())); err != nil {
				return err
			}
			continue
		}
		diskBytes += info.Size()
		total++
		if strings.HasPrefix(file.Name(), entry.cacheFileName()[:65]) {
			peerTotal++
		}
		if file.Name() == entry.cacheFileName() {
			previousBytes = info.Size()
		}
	}
	if (previousBytes == 0 && (total >= maxDatasetCatalogEntries || peerTotal >= maxDatasetCatalogPeerEntries)) || diskBytes-previousBytes+int64(buffer.Len()) > maxDatasetCatalogBytes {
		return errors.New("dataset catalog capacity reached")
	}
	file, err := os.CreateTemp(dir, ".pending-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(buffer.Bytes()); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func readDatasetCatalogArchive(path string, now time.Time) (result DatasetCatalogEntry, err error) {
	defer func() {
		if recover() != nil {
			result = DatasetCatalogEntry{}
			err = errors.New("malformed dataset catalog archive")
		}
	}()
	var empty DatasetCatalogEntry
	info, err := os.Lstat(path)
	if err != nil {
		return empty, err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxDatasetCatalogManifestBytes+65536+4096 {
		return empty, errors.New("invalid dataset catalog archive")
	}
	reader, err := zip.OpenReader(path)
	if err != nil {
		return empty, err
	}
	defer reader.Close()
	if len(reader.File) != 3 {
		return empty, errors.New("invalid dataset catalog archive entries")
	}
	parts := map[string][]byte{}
	for _, file := range reader.File {
		limit := 0
		switch file.Name {
		case "manifest.dpm":
			limit = MaxDatasetCatalogManifestBytes
		case "announcement.pnm":
			limit = 65536
		case "public-key.ed25519":
			limit = ed25519.PublicKeySize
		}
		if limit == 0 || file.Method != zip.Store || file.UncompressedSize64 > uint64(limit) || parts[file.Name] != nil {
			return empty, errors.New("invalid dataset catalog archive entry")
		}
		in, err := file.Open()
		if err != nil {
			return empty, err
		}
		data, err := io.ReadAll(io.LimitReader(in, int64(limit)+1))
		in.Close()
		if err != nil || len(data) > limit {
			return empty, errors.New("invalid dataset catalog archive bytes")
		}
		parts[file.Name] = data
	}
	manifest := parts["manifest.dpm"]
	// DecodeDatasetCatalog performs the safe, signed parse. An empty
	// expected publisher lets it use the signature-bound DPM identity.
	entry, err := DecodeDatasetCatalog(manifest, parts["public-key.ed25519"], "", now)
	if err != nil {
		return empty, err
	}
	proof, err := VerifySignedPNMEnvelopeWithProviderKey(parts["announcement.pnm"], parts["public-key.ed25519"])
	if err != nil || proof.CID != entry.ManifestCID || proof.FileID != entry.fileID {
		return empty, errors.New("cached announcement differs from manifest")
	}
	if filepath.Base(path) != entry.cacheFileName() {
		return empty, errors.New("cached manifest identity differs from path")
	}
	entry.PNMCID = storage.ComputeCID(parts["announcement.pnm"])
	return entry, nil
}

func ReadDatasetCatalog(store *storage.FlatSQLStore, now time.Time) ([]DatasetCatalogEntry, error) {
	if store == nil {
		return nil, errors.New("dataset catalog needs a store")
	}
	files, err := os.ReadDir(datasetCatalogPath(store))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	entries := make([]DatasetCatalogEntry, 0)
	for _, file := range files {
		if len(entries) >= maxDatasetCatalogEntries {
			break
		}
		if !strings.HasSuffix(file.Name(), ".zip") {
			continue
		}
		entry, err := readDatasetCatalogArchive(filepath.Join(datasetCatalogPath(store), file.Name()), now)
		if err != nil {
			continue
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].cacheFileName() < entries[j].cacheFileName() })
	return entries, nil
}

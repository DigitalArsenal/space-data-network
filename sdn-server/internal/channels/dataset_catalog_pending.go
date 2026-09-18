package channels

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// A pending announcement is a signed dataset-catalog announcement this node
// VERIFIED but could not act on yet — the single in-flight metadata fetch slot
// was already taken (Node.datasetCatalogMu). GossipSub does not republish an
// unchanged catalog, so dropping such an announcement loses that peer's
// catalog until it publishes a new one, which for a stable dataset may be
// never.
//
// This lane exists because the record store is no longer a substitute for it.
// Until 74c82735c the protocol stored every PNM announcement and the catch-up
// pass replayed skipped ones out of QueryRecentRecords("PNM.fbs"); that write
// is now gated on peer trust — correctly, because it lands on the anonymous
// public data plane that recordReadSourceFiltered unions across every producer
// table — so an untrusted peer's announcement has no durable home there and
// must not be given one.
//
// The lane is therefore a capped directory beside dataset-catalogs/, holding
// raw PNM bytes and nothing else:
//   - it is not a record table, so nothing here is ever served on the public
//     data plane;
//   - every byte in it has already passed verifyDatasetPublicationPNM against
//     the ANNOUNCER's own key, so it is not an anonymous-write surface;
//   - it is bounded three ways — per peer, in total, and by age — because an
//     untrusted peer can reach it and disk-fill is the obvious abuse.
const (
	// maxPendingDatasetAnnouncementBytes matches the PNM bound
	// cacheDatasetPublicationMetadata already enforces on the wire.
	maxPendingDatasetAnnouncementBytes = 65536

	// pendingDatasetAnnouncementTTL reaps an entry no catch-up pass ever
	// settled — a CID that never resolves, a provider that vanished. A day is
	// far longer than the catch-up interval, so a live announcement is never
	// reaped before it has been retried many times.
	pendingDatasetAnnouncementTTL = 24 * time.Hour

	pendingDatasetAnnouncementSuffix = ".pnm"
	pendingDatasetAnnouncementTemp   = ".pending-"
	// pendingPeerPrefixLength is the hex sha256 of the publisher peer ID plus
	// the "-" separator: the per-peer grouping key in a file name.
	pendingPeerPrefixLength = 2*sha256.Size + 1
	pendingNameLength       = pendingPeerPrefixLength + 2*sha256.Size + len(pendingDatasetAnnouncementSuffix)
)

// PendingDatasetAnnouncement is one parked announcement: the publisher that
// sent it and the raw signed PNM bytes.
type PendingDatasetAnnouncement struct {
	PeerID string
	PNM    []byte
}

func pendingDatasetCatalogPath(store *storage.FlatSQLStore) string {
	return filepath.Join(filepath.Dir(store.Path()), "dataset-catalogs-pending")
}

// pendingDatasetAnnouncementName binds the file name to BOTH identities in the
// record, so a renamed or swapped file fails the check in
// readPendingDatasetAnnouncement rather than replaying as a different peer's
// announcement. The peer half is a fixed-width prefix so the per-peer cap can
// be counted without opening a single file.
func pendingDatasetAnnouncementName(publisher string, pnm []byte) string {
	peerHash := sha256.Sum256([]byte(publisher))
	bodyHash := sha256.Sum256(pnm)
	return hex.EncodeToString(peerHash[:]) + "-" + hex.EncodeToString(bodyHash[:]) + pendingDatasetAnnouncementSuffix
}

func validPendingDatasetAnnouncement(publisher string, pnm []byte) error {
	if len(pnm) < 12 || len(pnm) > maxPendingDatasetAnnouncementBytes {
		return errors.New("pending dataset announcement exceeds catalog bounds")
	}
	if publisher == "" || len(publisher) > 256 || strings.ContainsAny(publisher, "\n\x00/\\") {
		return errors.New("invalid pending dataset announcement publisher")
	}
	return nil
}

type pendingDatasetAnnouncementFile struct {
	name     string
	modified time.Time
	size     int64
}

// scanPendingDatasetAnnouncements lists the lane, dropping interrupted temp
// files and entries past the TTL as it goes. Reaping on every pass is what
// keeps the caps meaningful: without it a burst of dead announcements would
// hold slots against live ones for as long as the directory existed.
func scanPendingDatasetAnnouncements(dir string, now time.Time) ([]pendingDatasetAnnouncementFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := make([]pendingDatasetAnnouncementFile, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		info, err := entry.Info()
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		// Only this writer creates the temp names, and only between CreateTemp
		// and Rename. One left behind is an interrupted write, never admitted.
		if !info.Mode().IsRegular() ||
			strings.HasPrefix(name, pendingDatasetAnnouncementTemp) ||
			len(name) != pendingNameLength ||
			!strings.HasSuffix(name, pendingDatasetAnnouncementSuffix) ||
			info.Size() > maxPendingDatasetAnnouncementBytes+512 ||
			now.Sub(info.ModTime()) > pendingDatasetAnnouncementTTL {
			_ = os.Remove(filepath.Join(dir, name))
			continue
		}
		files = append(files, pendingDatasetAnnouncementFile{name: name, modified: info.ModTime(), size: info.Size()})
	}
	return files, nil
}

func oldestPendingFirst(files []pendingDatasetAnnouncementFile) {
	sort.Slice(files, func(i, j int) bool { return files[i].modified.Before(files[j].modified) })
}

// RememberPendingDatasetAnnouncement parks a verified announcement for a later
// catch-up pass. Re-parking one already present is a no-op, so a peer that
// keeps re-announcing the same catalog occupies one slot, not one per message.
//
// Admission is capped per peer FIRST: a peer at its own cap evicts only its own
// oldest entry, so no peer can push another peer's announcements out until the
// directory as a whole is full. Once it is, the globally oldest entry goes —
// the newest announcements are the ones still worth a fetch.
func RememberPendingDatasetAnnouncement(store *storage.FlatSQLStore, publisher string, pnm []byte, now time.Time) error {
	if store == nil {
		return errors.New("pending dataset announcement needs a store")
	}
	if err := validPendingDatasetAnnouncement(publisher, pnm); err != nil {
		return err
	}
	dir := pendingDatasetCatalogPath(store)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	name := pendingDatasetAnnouncementName(publisher, pnm)
	files, err := scanPendingDatasetAnnouncements(dir, now)
	if err != nil {
		return err
	}
	peerPrefix := name[:pendingPeerPrefixLength]
	var peerFiles, allFiles []pendingDatasetAnnouncementFile
	var diskBytes int64
	for _, file := range files {
		if file.name == name {
			// Already parked. Its mtime is deliberately left alone: a peer
			// re-announcing the same unfetchable CID must not be able to hold
			// a slot past the TTL by repeating itself.
			return nil
		}
		diskBytes += file.size
		allFiles = append(allFiles, file)
		if strings.HasPrefix(file.name, peerPrefix) {
			peerFiles = append(peerFiles, file)
		}
	}
	admitBytes := int64(len(publisher)) + 1 + int64(len(pnm))
	for len(peerFiles) >= maxDatasetCatalogPeerEntries {
		oldestPendingFirst(peerFiles)
		evicted := peerFiles[0]
		if err := os.Remove(filepath.Join(dir, evicted.name)); err != nil && !os.IsNotExist(err) {
			return err
		}
		peerFiles = peerFiles[1:]
		diskBytes -= evicted.size
		for i, file := range allFiles {
			if file.name == evicted.name {
				allFiles = append(allFiles[:i], allFiles[i+1:]...)
				break
			}
		}
	}
	for len(allFiles) >= maxDatasetCatalogEntries || diskBytes+admitBytes > maxDatasetCatalogBytes {
		if len(allFiles) == 0 {
			return errors.New("pending dataset announcement capacity reached")
		}
		oldestPendingFirst(allFiles)
		if err := os.Remove(filepath.Join(dir, allFiles[0].name)); err != nil && !os.IsNotExist(err) {
			return err
		}
		diskBytes -= allFiles[0].size
		allFiles = allFiles[1:]
	}

	// publisher + "\n" + pnm. The publisher is a peer ID string, which cannot
	// contain a newline (validPendingDatasetAnnouncement rejects one anyway),
	// and the PNM is an opaque size-prefixed FlatBuffer that is never scanned
	// for the separator.
	body := make([]byte, 0, admitBytes)
	body = append(body, publisher...)
	body = append(body, '\n')
	body = append(body, pnm...)

	file, err := os.CreateTemp(dir, pendingDatasetAnnouncementTemp)
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(body); err != nil {
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
	if err := os.Rename(file.Name(), filepath.Join(dir, name)); err != nil {
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func readPendingDatasetAnnouncement(dir, name string) (PendingDatasetAnnouncement, error) {
	var empty PendingDatasetAnnouncement
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return empty, err
	}
	if len(raw) > maxPendingDatasetAnnouncementBytes+512 {
		return empty, errors.New("pending dataset announcement exceeds catalog bounds")
	}
	separator := bytes.IndexByte(raw, '\n')
	if separator < 0 {
		return empty, errors.New("malformed pending dataset announcement")
	}
	entry := PendingDatasetAnnouncement{PeerID: string(raw[:separator]), PNM: raw[separator+1:]}
	if err := validPendingDatasetAnnouncement(entry.PeerID, entry.PNM); err != nil {
		return empty, err
	}
	// The name is derived from both halves, so this rejects a file whose
	// contents were swapped for another peer's or another announcement's.
	if pendingDatasetAnnouncementName(entry.PeerID, entry.PNM) != name {
		return empty, errors.New("pending dataset announcement identity differs from path")
	}
	return entry, nil
}

// ReadPendingDatasetAnnouncements returns the parked announcements, oldest
// first, dropping any file that fails its own identity check. Oldest first is
// the order a catch-up pass should retry them in: an announcement that has
// waited longest has had the most chances taken from it.
func ReadPendingDatasetAnnouncements(store *storage.FlatSQLStore, now time.Time) ([]PendingDatasetAnnouncement, error) {
	if store == nil {
		return nil, errors.New("pending dataset announcements need a store")
	}
	dir := pendingDatasetCatalogPath(store)
	files, err := scanPendingDatasetAnnouncements(dir, now)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	oldestPendingFirst(files)
	entries := make([]PendingDatasetAnnouncement, 0, len(files))
	for _, file := range files {
		if len(entries) >= maxDatasetCatalogEntries {
			break
		}
		entry, err := readPendingDatasetAnnouncement(dir, file.name)
		if err != nil {
			_ = os.Remove(filepath.Join(dir, file.name))
			continue
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// ForgetPendingDatasetAnnouncement drops an announcement the catch-up pass
// settled — cached, already cached, superseded, or permanently unusable.
func ForgetPendingDatasetAnnouncement(store *storage.FlatSQLStore, publisher string, pnm []byte) error {
	if store == nil {
		return errors.New("pending dataset announcement needs a store")
	}
	if err := validPendingDatasetAnnouncement(publisher, pnm); err != nil {
		return err
	}
	path := filepath.Join(pendingDatasetCatalogPath(store), pendingDatasetAnnouncementName(publisher, pnm))
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

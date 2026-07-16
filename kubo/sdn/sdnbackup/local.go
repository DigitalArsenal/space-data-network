package sdnbackup

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// LocalAdapter is the test-friendly reference adapter: a filesystem-backed,
// content-addressed store needing no external credentials. Each blob is written
// as its $MBL envelope (BlobToMBL) at <root>/sdn-backup/<kind>/<hh>/<hash>, so
// it proves the exact envelope round-trip an HTTPS provider would perform. It
// implements every Adapter method, including Delete.
type LocalAdapter struct {
	root       string
	providerID string
}

var _ Adapter = (*LocalAdapter)(nil)

// NewLocalAdapter opens (creating if needed) a filesystem adapter rooted at
// dir.
func NewLocalAdapter(dir, providerID string) (*LocalAdapter, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, fmt.Errorf("sdnbackup: local adapter requires a root dir")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("sdnbackup: create local adapter root %q: %w", dir, err)
	}
	if providerID == "" {
		providerID = "local"
	}
	return &LocalAdapter{root: dir, providerID: providerID}, nil
}

func (a *LocalAdapter) Describe(ctx context.Context) (AdapterDescriptor, error) {
	return AdapterDescriptor{
		ProviderID: a.providerID,
		Capabilities: AdapterCapabilities{
			Put: true, Get: true, Has: true, List: true, Delete: true,
			Versioning: false, NativeHash: true,
		},
		MaxBlobSize:      0,
		CredentialLane:   "",
		AddressingScheme: "content-hash/sdn-backup",
	}, nil
}

// pathFor resolves the on-disk path for a blob of the given kind + hash.
func (a *LocalAdapter) pathFor(kind Kind, contentHash string) (string, error) {
	key, err := ObjectKey(kind, contentHash)
	if err != nil {
		return "", err
	}
	return filepath.Join(a.root, filepath.FromSlash(key)), nil
}

// locate finds a blob's path. With a kind hint it forms the exact key; without
// one it scans the kind subdirectories under the hash's fan-out prefix.
func (a *LocalAdapter) locate(ref BlobRef) (path string, kind Kind, err error) {
	h, err := NormalizeContentHash(ref.ContentHash)
	if err != nil {
		return "", "", err
	}
	if ref.Kind != "" {
		p, err := a.pathFor(ref.Kind, h)
		if err != nil {
			return "", "", err
		}
		return p, ref.Kind, nil
	}
	// Scan sdn-backup/<kind>/<hh>/<hash> across known kinds.
	for k := range knownKinds {
		p := filepath.Join(a.root, keyPrefix, string(k), h[:2], h)
		if _, statErr := os.Stat(p); statErr == nil {
			return p, k, nil
		}
	}
	return "", "", adapterErr(ErrNotFound, "locate", "no object for content hash %s", h)
}

func (a *LocalAdapter) Put(ctx context.Context, blob BackupBlob) (PutAck, error) {
	path, err := a.pathFor(blob.Kind, blob.ContentHash)
	if err != nil {
		return PutAck{}, err
	}
	key, _ := ObjectKey(blob.Kind, blob.ContentHash)
	if info, statErr := os.Stat(path); statErr == nil {
		// Idempotent: byte-identical content is already present (the key IS the
		// hash), so this is a no-op that still acks.
		return PutAck{ContentHash: blob.ContentHash, ProviderKey: key, SizeStored: int(info.Size()), AlreadyPresent: true}, nil
	}
	env, err := BlobToMBL(blob)
	if err != nil {
		return PutAck{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return PutAck{}, adapterErr(ErrProvider, "put", "mkdir: %v", err)
	}
	if err := writeFileAtomic(path, env); err != nil {
		return PutAck{}, adapterErr(ErrProvider, "put", "write: %v", err)
	}
	return PutAck{ContentHash: blob.ContentHash, ProviderKey: key, SizeStored: len(env)}, nil
}

func (a *LocalAdapter) Get(ctx context.Context, ref BlobRef) (BackupBlob, error) {
	path, _, err := a.locate(ref)
	if err != nil {
		return BackupBlob{}, err
	}
	env, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return BackupBlob{}, adapterErr(ErrNotFound, "get", "no object at %s", path)
		}
		return BackupBlob{}, adapterErr(ErrProvider, "get", "read: %v", err)
	}
	blob, err := BlobFromMBL(env)
	if err != nil {
		return BackupBlob{}, err
	}
	return blob, nil
}

func (a *LocalAdapter) Has(ctx context.Context, ref BlobRef) (Presence, error) {
	path, _, err := a.locate(ref)
	if err != nil {
		if IsNotFound(err) {
			return Presence{ContentHash: ref.ContentHash, Present: false}, nil
		}
		return Presence{}, err
	}
	if _, statErr := os.Stat(path); statErr != nil {
		return Presence{ContentHash: ref.ContentHash, Present: false}, nil
	}
	return Presence{ContentHash: ref.ContentHash, Present: true}, nil
}

func (a *LocalAdapter) List(ctx context.Context, q ListQuery) (ListPage, error) {
	base := filepath.Join(a.root, keyPrefix)
	var page ListPage
	err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil // empty store
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(a.root, p)
		if relErr != nil {
			return nil
		}
		key := filepath.ToSlash(rel)
		kind, hash, ok := parseObjectKey(key)
		if !ok {
			return nil
		}
		if q.KindFilter != "" && kind != q.KindFilter {
			return nil
		}
		if q.Prefix != "" && !strings.HasPrefix(key, q.Prefix) {
			return nil
		}
		info, infoErr := d.Info()
		size := 0
		if infoErr == nil {
			size = int(info.Size())
		}
		page.Entries = append(page.Entries, ListEntry{
			ContentHash: hash,
			Kind:        kind,
			Size:        size,
			ProviderKey: key,
		})
		return nil
	})
	if err != nil {
		return ListPage{}, adapterErr(ErrProvider, "list", "walk: %v", err)
	}
	return page, nil
}

func (a *LocalAdapter) Delete(ctx context.Context, ref BlobRef) (DeleteAck, error) {
	path, _, err := a.locate(ref)
	if err != nil {
		if IsNotFound(err) {
			return DeleteAck{ContentHash: ref.ContentHash, Deleted: false}, nil
		}
		return DeleteAck{}, err
	}
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return DeleteAck{ContentHash: ref.ContentHash, Deleted: false}, nil
		}
		return DeleteAck{}, adapterErr(ErrProvider, "delete", "remove: %v", err)
	}
	return DeleteAck{ContentHash: ref.ContentHash, Deleted: true}, nil
}

// parseObjectKey splits sdn-backup/<kind>/<hh>/<hash> back into its kind + hash.
func parseObjectKey(key string) (Kind, string, bool) {
	parts := strings.Split(key, "/")
	if len(parts) != 4 || parts[0] != "sdn-backup" {
		return "", "", false
	}
	kind := Kind(parts[1])
	if !knownKinds[kind] {
		return "", "", false
	}
	hash, err := NormalizeContentHash(parts[3])
	if err != nil {
		return "", "", false
	}
	return kind, hash, true
}

// writeFileAtomic writes data to path via a temp file + rename.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

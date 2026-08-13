package main

// ipfsProfilePhotoStore binds internal/auth's ProfilePhotoStore port to the
// IPFS lane this node already runs.
//
// THE BOUNDARY THIS FILE KEEPS: internal/auth knows nothing about Kubo, and the
// storage package gains no knowledge of accounts. The only new thing in the
// node is this adapter — a connector, which is the one kind of code the host is
// allowed to grow. No new capability, no new RPC surface, no application logic:
// bytes in, content identifier out, through the same pin path the asset lane
// has always used.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

type ipfsProfilePhotoStore struct {
	apiURL string
}

// PinProfilePhoto stores bytes as a pinned UnixFS object and returns its CID.
//
// The bytes reach Kubo through a temporary file because the node's pin helper
// is path-based. The file is created with 0600 in the process's own temp dir
// and removed on every path out, including failure: a profile photo is not
// sensitive, but leaving other people's faces in /tmp on a shared box is not
// this node's habit.
func (s ipfsProfilePhotoStore) PinProfilePhoto(ctx context.Context, data []byte, contentType string) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("no image bytes to store")
	}

	dir, err := os.MkdirTemp("", "sdn-profile-photo-")
	if err != nil {
		return "", fmt.Errorf("create temporary directory for profile photo: %w", err)
	}
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, "photo"+profilePhotoExtension(contentType))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("stage profile photo: %w", err)
	}

	cid, err := storage.PinAssetGLB(ctx, s.apiURL, path)
	if err != nil {
		return "", fmt.Errorf("pin profile photo: %w", err)
	}
	return cid, nil
}

// profilePhotoExtension keeps the staged filename honest about its contents.
// The extension does not reach the CID (UnixFS hashes the bytes, not the name)
// — it exists so a failed pin leaves a diagnosable file behind in logs.
func profilePhotoExtension(contentType string) string {
	switch contentType {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	default:
		return ".bin"
	}
}

package main

// ipfsProfilePhotoStore binds internal/auth's ProfilePhotoStore port to the
// node pin policy (PROFILE_PHOTO): internal/auth knows nothing about Kubo, and
// the pin policy knows nothing about accounts. Bytes in, content identifier
// out; the photo is pinned only when the policy pins profile photos.

import (
	"context"
	"fmt"

	"github.com/spacedatanetwork/sdn-server/internal/pinpolicy"
)

type ipfsProfilePhotoStore struct {
	pins *pinpolicy.Policy
}

// PinProfilePhoto adds the photo as a CIDv1 UnixFS file (256 KiB chunks, the
// asset lane's addressing) and returns its CID.
func (s ipfsProfilePhotoStore) PinProfilePhoto(ctx context.Context, data []byte, contentType string) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("no image bytes to store")
	}
	added, err := s.pins.Scope(pinpolicy.ProfilePhoto, "").AddBytes(ctx, "photo"+profilePhotoExtension(contentType), data,
		pinpolicy.AddOptions{CIDv1: true, Chunker: "size-262144"})
	if err != nil {
		return "", fmt.Errorf("store profile photo: %w", err)
	}
	return added.CID, nil
}

// profilePhotoExtension keeps the added file's name honest about its contents.
// The extension does not reach the CID (UnixFS hashes the bytes, not the name).
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

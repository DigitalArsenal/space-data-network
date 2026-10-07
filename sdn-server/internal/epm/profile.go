package epm

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"strings"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
)

const EPMContentType = "application/x-flatbuffers"

var ErrInvalidProfileEPM = errors.New("invalid EPM FlatBuffer")

// DecodeProfileEPM validates the size-prefixed $EPM envelope before reading
// the operator-editable profile fields. Callers may safely report the returned
// error as rejected input; malformed FlatBuffers are converted from panics.
func DecodeProfileEPM(data []byte) (profile *Profile, err error) {
	if len(data) < 12 {
		return nil, fmt.Errorf("%w: record is too short", ErrInvalidProfileEPM)
	}
	if int(binary.LittleEndian.Uint32(data[:4])) != len(data)-4 {
		return nil, fmt.Errorf("%w: size prefix does not match the body", ErrInvalidProfileEPM)
	}
	if !EPM.SizePrefixedEPMBufferHasIdentifier(data) {
		return nil, fmt.Errorf("%w: missing $EPM identifier", ErrInvalidProfileEPM)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			profile = nil
			err = fmt.Errorf("%w: unreadable record", ErrInvalidProfileEPM)
		}
	}()
	profile, err = ProfileFromEPMBytes(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidProfileEPM, err)
	}
	return profile, nil
}

// UpdateProfileFromEPM accepts the wire contract used by the identity editor.
// The schema has no editable key-path fields, so the stored paths survive a
// wire update. A record that carries a photo sets it (checked like
// SetNodePhoto); one without keeps the stored photo, so an editor whose
// bindings predate the field cannot erase it — removal is PUT
// /api/node/epm/photo with an empty value.
func (s *Service) UpdateProfileFromEPM(data []byte) error {
	profile, err := DecodeProfileEPM(data)
	if err != nil {
		return err
	}
	if strings.TrimSpace(profile.PhotoDataURL) != "" {
		photo, err := NodePhotoDataURL(profile.PhotoDataURL)
		if err != nil {
			return err
		}
		profile.PhotoDataURL = photo
	}
	if current := s.GetNodeProfile(); current != nil {
		if strings.TrimSpace(profile.PhotoDataURL) == "" {
			profile.PhotoDataURL = current.PhotoDataURL
		}
		profile.SigningKeyPath = current.SigningKeyPath
		profile.EncryptionKeyPath = current.EncryptionKeyPath
	}
	return s.UpdateProfile(profile)
}

// EncodeProfileEPM serialises a profile as an unsigned, size-prefixed $EPM
// FlatBuffer: the wire form PUT /api/node/epm accepts. The node re-signs and
// re-keys the record itself, so key and signature fields are left empty.
func EncodeProfileEPM(profile *Profile) ([]byte, error) {
	if profile == nil {
		profile = &Profile{}
	}
	scratch := &Service{profile: profile}
	return scratch.buildEPMBytesLocked("", 0)
}

// MaxNodePhotoBytes bounds the node's photo. It rides the signed $EPM every
// resolving peer fetches, so it stays small: the dashboard sends a 256 px
// JPEG of at most 24 KB.
const MaxNodePhotoBytes = 64 << 10

// maxNodePhotoEdge bounds either side of the photo in pixels.
const maxNodePhotoEdge = 2048

// ErrInvalidNodePhoto is a photo the node will not put on its card.
var ErrInvalidNodePhoto = errors.New("invalid node photo")

// NodePhotoDataURL checks a photo for the node's card: a base64 data URL of
// a JPEG or PNG that decodes as an image, within MaxNodePhotoBytes. An empty
// value means no photo and passes as is.
func NodePhotoDataURL(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	header, payload, ok := strings.Cut(value, ",")
	mediaType := strings.TrimPrefix(strings.TrimSuffix(header, ";base64"), "data:")
	if !ok || !strings.HasPrefix(header, "data:") || !strings.HasSuffix(header, ";base64") ||
		(mediaType != "image/jpeg" && mediaType != "image/png") {
		return "", fmt.Errorf("%w: send a base64 data URL of a JPEG or PNG", ErrInvalidNodePhoto)
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "", fmt.Errorf("%w: the image is not valid base64", ErrInvalidNodePhoto)
	}
	if len(data) > MaxNodePhotoBytes {
		return "", fmt.Errorf("%w: the image is %d bytes; the limit is %d", ErrInvalidNodePhoto, len(data), MaxNodePhotoBytes)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || "image/"+format != mediaType {
		return "", fmt.Errorf("%w: the bytes are not a %s image", ErrInvalidNodePhoto, mediaType)
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > maxNodePhotoEdge || config.Height > maxNodePhotoEdge {
		return "", fmt.Errorf("%w: the image is %dx%d; each side must be 1-%d px", ErrInvalidNodePhoto, config.Width, config.Height, maxNodePhotoEdge)
	}
	return "data:" + mediaType + ";base64," + payload, nil
}

// SetNodePhoto replaces the node's photo (owner 2026-10-07: an uploaded photo
// "did NOT replace the image and did not survive the refresh"). The photo is
// the record's PHOTO field (SDS 1.239.0): the profile update re-signs and
// persists the record, so the node reads it back after a restart, peers see
// it, and the cards carry it. An empty value removes it.
func (s *Service) SetNodePhoto(dataURL string) error {
	photo, err := NodePhotoDataURL(dataURL)
	if err != nil {
		return err
	}
	profile := s.GetNodeProfile()
	if profile == nil {
		profile = &Profile{}
	}
	profile.PhotoDataURL = photo
	return s.UpdateProfile(profile)
}

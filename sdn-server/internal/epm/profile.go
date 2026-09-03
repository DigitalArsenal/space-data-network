package epm

import (
	"encoding/binary"
	"errors"
	"fmt"

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
// The current schema has no photo or editable key-path profile fields, so the
// stored values for those fields survive a wire update instead of being reset.
func (s *Service) UpdateProfileFromEPM(data []byte) error {
	profile, err := DecodeProfileEPM(data)
	if err != nil {
		return err
	}
	if current := s.GetNodeProfile(); current != nil {
		profile.PhotoDataURL = current.PhotoDataURL
		profile.SigningKeyPath = current.SigningKeyPath
		profile.EncryptionKeyPath = current.EncryptionKeyPath
	}
	return s.UpdateProfile(profile)
}

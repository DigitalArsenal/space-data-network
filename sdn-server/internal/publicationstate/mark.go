package publicationstate

import (
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"

	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/fbverify"
	"github.com/spacedatanetwork/sdn-server/internal/publicationstate/pmk"
)

// The mark of a running or refused dataset publication has a schema of its
// own: PublicationState.fbs is part of what every publication's manifest
// signs (SchemaSHA256), so it does not change for node-internal records.
//
//go:embed schema/PublicationMark.fbs
var markSchema []byte

const markSchemaName = "PublicationMark.fbs"

var markRegistry = func() *fbverify.Registry {
	r := fbverify.NewRegistry()
	r.AddSource(markSchemaName, markSchema)
	return r
}()

// EncodeMark produces one size-prefixed $PMK FlatBuffer.
func EncodeMark(mark *pmk.PublicationMarkT) []byte {
	b := flatbuffers.NewBuilder(256)
	b.FinishSizePrefixedWithFileIdentifier(mark.Pack(b), []byte("$PMK"))
	return b.FinishedBytes()
}

// DecodeMark verifies the complete structure before unpacking fields.
func DecodeMark(data []byte) (*pmk.PublicationMarkT, error) {
	if len(data) < 12 || uint64(binary.LittleEndian.Uint32(data)) != uint64(len(data)-4) || string(data[8:12]) != "$PMK" {
		return nil, errors.New("invalid publication mark length or identifier")
	}
	if err := markRegistry.VerifySizePrefixedBuffer(markSchemaName, data); err != nil {
		return nil, fmt.Errorf("invalid publication mark: %w", err)
	}
	return pmk.GetSizePrefixedRootAsPublicationMark(data, 0).UnPack(), nil
}

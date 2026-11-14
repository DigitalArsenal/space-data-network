// Package publicationstate encodes node-internal publication metadata.
package publicationstate

import (
 "bytes"
 "crypto/sha256"
 _ "embed"
 "encoding/binary"
 "errors"
 "fmt"

 flatbuffers "github.com/google/flatbuffers/go"
 "github.com/spacedatanetwork/sdn-server/internal/fbverify"
 "github.com/spacedatanetwork/sdn-server/internal/publicationstate/pst"
)

//go:embed schema/PublicationState.fbs
var schema []byte

var registry = func() *fbverify.Registry {
 r := fbverify.NewRegistry()
 r.AddSource("PublicationState.fbs", schema)
 return r
}()

// Encode produces one canonical size-prefixed, checksummed FlatBuffer.
func Encode(body *pst.BodyT) []byte {
 b := flatbuffers.NewBuilder(256)
 root := (&pst.PublicationStateT{Version: 1, Payload: body, Checksum: make([]byte, sha256.Size)}).Pack(b)
 b.FinishSizePrefixedWithFileIdentifier(root, []byte("$PST"))
 data := b.FinishedBytes()
 sum := sha256.Sum256(data)
 copy(pst.GetSizePrefixedRootAsPublicationState(data, 0).ChecksumBytes(), sum[:])
 return data
}

// Decode verifies the complete structure and checksum before unpacking fields.
// The checksum covers padding and the length prefix as well as every field.
func Decode(data []byte, kind pst.Body) (*pst.BodyT, error) {
 if len(data) < 16 || uint64(binary.LittleEndian.Uint32(data)) != uint64(len(data)-4) || string(data[8:12]) != "$PST" {
  return nil, errors.New("invalid publication FlatBuffer length or identifier")
 }
 if err := registry.VerifySizePrefixedBuffer("PublicationState.fbs", data); err != nil {
  return nil, fmt.Errorf("invalid publication FlatBuffer: %w", err)
 }
 root := pst.GetSizePrefixedRootAsPublicationState(data, 0)
 if root.Version() != 1 || root.PayloadType() != kind || kind == pst.BodyNONE || len(root.ChecksumBytes()) != sha256.Size {
  return nil, errors.New("invalid publication FlatBuffer version, kind or checksum length")
 }
 // Hash without altering the caller's buffer.
 tab := root.Table()
 start := int(tab.Vector(tab.Offset(10)))
 h := sha256.New()
 h.Write(data[:start])
 h.Write(make([]byte, sha256.Size))
 h.Write(data[start+sha256.Size:])
 if !bytes.Equal(root.ChecksumBytes(), h.Sum(nil)) {
  return nil, errors.New("publication FlatBuffer checksum mismatch")
 }
 body := root.UnPack().Payload
 if body == nil || body.Value == nil {
  return nil, errors.New("publication FlatBuffer payload missing")
 }
 return body, nil
}

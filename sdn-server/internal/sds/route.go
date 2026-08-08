package sds

import (
	"encoding/binary"
	"fmt"
)

// Header-only routing: the buffer says what it is.
//
// The engine has always routed this way — flatsql's StreamingFlatBufferStore
// reads four bytes at FILE_IDENTIFIER_OFFSET and does ONE hash lookup, no
// vtable walk, no table traversal — and sdn-js does too
// (flatBufferMatchesFileId checks both offsets). The Go host did not: the type
// arrived as an out-of-band caller-declared string (a libp2p length-prefixed
// field, an HTTP URL path segment, a pubsub topic) and the embedded
// file_identifier was read only to VALIDATE that declaration. The wire was
// therefore not self-describing at the host boundary: a transport with no
// schema channel could not deliver a record at all, and one exception
// (PNM-on-any-topic) had to be hand-written to work around it.
//
// This file makes the header the authority and the declared name a cross-check.
// Cost is one uint32 read plus a 4-byte compare and one map lookup per record —
// the same work the engine already does, and strictly less than the parse the
// validator runs immediately afterwards.
//
// Two conventions exist and BOTH are handled, because both are on the wire:
//   - size-prefixed frame (the canonical SDN form): identifier at [8..12)
//   - bare finished buffer (producers that call Finish directly): [4..8)
//
// Deliberately NOT a union-ordinal or table-field decision: Record.standard /
// file_identifier discipline only. An ordinal is a position in a generated
// enum, which changes meaning when the IDL is edited; a file_identifier is the
// standard's own four bytes, carried in the record.

// FileIdentifierFromBuffer reads the FlatBuffers file_identifier out of a
// buffer head without decoding anything. It accepts both wire conventions and
// reports false when the buffer is too short to carry one.
//
// The size prefix is the discriminator, exactly as in the engine: a leading
// uint32 that EXACTLY accounts for the remaining bytes means "size-prefixed
// frame", and the identifier is one word further in. This is the same test
// sizePrefixedPayload applies, kept separate so routing never depends on
// validation having run.
func FileIdentifierFromBuffer(data []byte) (string, bool) {
	if len(data) >= sizePrefixLength+minFlatBufferLength {
		size := binary.LittleEndian.Uint32(data[:sizePrefixLength])
		if int64(size) == int64(len(data))-sizePrefixLength {
			return string(data[sizePrefixLength+sizePrefixLength : sizePrefixLength+sizePrefixLength+fileIdentifierLength]), true
		}
	}
	if len(data) >= minFlatBufferLength {
		return string(data[sizePrefixLength : sizePrefixLength+fileIdentifierLength]), true
	}
	return "", false
}

// SchemaForIdentifier resolves a four-byte file_identifier to the schema that
// declares it.
//
// Ambiguity is refused rather than guessed: if two loaded schemas ever declare
// the same identifier there is no in-band way to choose between them, so the
// reverse index drops the entry and routing falls back to the declared name.
// (Measured on the 206 embedded schemas at the time of writing: 205 declare an
// identifier and every one of them is unique.)
func (v *Validator) SchemaForIdentifier(identifier string) (string, bool) {
	if v == nil || len(identifier) != fileIdentifierLength {
		return "", false
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	name, ok := v.identifierSchemas[identifier]
	return name, ok
}

// RouteDecision is the outcome of routing one buffer at the host boundary.
type RouteDecision struct {
	// Schema is the schema the record MUST be validated and stored under.
	Schema string
	// Identifier is the file_identifier read from the buffer head, if any.
	Identifier string
	// FromHeader is true when Schema was derived from the buffer's own bytes.
	// False means the header carried no identifier this node knows, and the
	// caller's declared name was used instead.
	FromHeader bool
	// Declared is the caller's out-of-band schema name, verbatim (possibly "").
	Declared string
	// Mismatch is true when a non-empty declared name disagrees with the
	// header. It is NEVER resolved in favour of the declaration.
	Mismatch bool
}

// RouteBuffer routes a record on its own header, using the caller's declared
// schema name only as a cross-check.
//
// Rules, in order:
//  1. Read the identifier from the buffer head (no decode). If it maps to a
//     loaded schema, THAT is the route.
//  2. If the declared name disagrees with the header route, say so
//     (Mismatch) — the header still wins. Callers on a lane where the
//     declaration is a COMMITMENT (an HTTP publish path, a libp2p push field)
//     must treat Mismatch as an error; callers on a lane where it is merely a
//     delivery CHANNEL (a pubsub topic) may proceed on the header, which is
//     how a $PNM announcement legitimately arrives on another standard's
//     topic.
//  3. If the buffer carries no identifier this node knows, fall back to the
//     declared name. Nothing is admitted on that path that was not admitted
//     before: VerifyEnvelope still requires the declared schema's identifier
//     to be present, so an unknown-identifier buffer fails validation exactly
//     as it always did.
//
// An error is returned only when there is nothing to route on at all.
func (v *Validator) RouteBuffer(declared string, data []byte) (RouteDecision, error) {
	decision := RouteDecision{Declared: declared, Schema: declared}
	if identifier, ok := FileIdentifierFromBuffer(data); ok {
		decision.Identifier = identifier
		if schema, known := v.SchemaForIdentifier(identifier); known {
			decision.Schema = schema
			decision.FromHeader = true
			decision.Mismatch = declared != "" && declared != schema
			return decision, nil
		}
	}
	if declared == "" {
		return decision, fmt.Errorf(
			"cannot route record: no schema declared and %s", describeIdentifier(data))
	}
	return decision, nil
}

// MismatchError renders the routing disagreement for a lane that treats the
// declared name as a commitment. Returns nil when there is no mismatch.
func (d RouteDecision) MismatchError() error {
	if !d.Mismatch {
		return nil
	}
	return fmt.Errorf(
		"schema mismatch: caller declared %s but the record's own file identifier %q is %s",
		d.Declared, d.Identifier, d.Schema)
}

package sds

import (
	"encoding/binary"
	"strings"
	"testing"
)

// realOMM builds a genuine size-prefixed $OMM record through the same builder
// the node uses, so these tests read the SAME bytes a peer would send.
func realOMM(t *testing.T) []byte {
	t.Helper()
	data := NewOMMBuilder().
		WithObjectName("ISS (ZARYA)").
		WithNoradCatID(25544).
		WithEpoch("2026-08-07T00:00:00.000Z").
		Build()
	if len(data) < 12 {
		t.Fatalf("built OMM is %d bytes", len(data))
	}
	return data
}

func newLoadedValidator(t *testing.T) *Validator {
	t.Helper()
	v, err := NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	return v
}

func TestFileIdentifierFromBufferBothConventions(t *testing.T) {
	prefixed := realOMM(t)
	if got, ok := FileIdentifierFromBuffer(prefixed); !ok || got != "$OMM" {
		t.Fatalf("size-prefixed identifier = %q ok=%v, want $OMM true", got, ok)
	}
	// The bare finished buffer carries its identifier one word earlier. The
	// module-sdk parity fixtures are in exactly this shape.
	bare := prefixed[4:]
	if got, ok := FileIdentifierFromBuffer(bare); !ok || got != "$OMM" {
		t.Fatalf("bare identifier = %q ok=%v, want $OMM true", got, ok)
	}
	if _, ok := FileIdentifierFromBuffer([]byte{1, 2, 3}); ok {
		t.Fatal("a 3-byte buffer must not yield an identifier")
	}
}

// A record whose size prefix does not account for the remaining bytes must NOT
// be read as size-prefixed: that is the exact confusion that would slide the
// identifier read four bytes and route the record to the wrong table.
func TestFileIdentifierRejectsWrongSizePrefix(t *testing.T) {
	data := append([]byte(nil), realOMM(t)...)
	binary.LittleEndian.PutUint32(data[:4], uint32(len(data)))
	if got, _ := FileIdentifierFromBuffer(data); got == "$OMM" {
		t.Fatal("a mis-stated size prefix must not be treated as a valid frame")
	}
}

func TestRouteBufferRoutesOnHeaderWithNoDeclaration(t *testing.T) {
	v := newLoadedValidator(t)
	decision, err := v.RouteBuffer("", realOMM(t))
	if err != nil {
		t.Fatalf("RouteBuffer: %v", err)
	}
	if decision.Schema != "OMM.fbs" || !decision.FromHeader {
		t.Fatalf("routed to %q fromHeader=%v, want OMM.fbs true", decision.Schema, decision.FromHeader)
	}
	if decision.Mismatch {
		t.Fatal("an empty declaration can never mismatch")
	}
}

func TestRouteBufferAgreementIsSilent(t *testing.T) {
	v := newLoadedValidator(t)
	decision, err := v.RouteBuffer("OMM.fbs", realOMM(t))
	if err != nil {
		t.Fatalf("RouteBuffer: %v", err)
	}
	if decision.Mismatch || decision.Schema != "OMM.fbs" {
		t.Fatalf("decision = %+v, want OMM.fbs with no mismatch", decision)
	}
	if err := decision.MismatchError(); err != nil {
		t.Fatalf("MismatchError = %v, want nil", err)
	}
}

// The whole point: the declaration is a cross-check, never the authority.
func TestRouteBufferHeaderBeatsDeclaration(t *testing.T) {
	v := newLoadedValidator(t)
	decision, err := v.RouteBuffer("CAT.fbs", realOMM(t))
	if err != nil {
		t.Fatalf("RouteBuffer: %v", err)
	}
	if decision.Schema != "OMM.fbs" {
		t.Fatalf("routed to %q, want OMM.fbs (the header wins)", decision.Schema)
	}
	if !decision.Mismatch {
		t.Fatal("declared CAT.fbs against an $OMM header must report a mismatch")
	}
	err = decision.MismatchError()
	if err == nil {
		t.Fatal("a commitment lane must get an error for a mismatch")
	}
	for _, want := range []string{"CAT.fbs", "$OMM", "OMM.fbs"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("mismatch error %q does not name %q", err, want)
		}
	}
}

// An identifier this node does not know cannot route, so the declared name is
// used and the existing envelope check still refuses the record. Nothing is
// admitted that was not admitted before.
func TestRouteBufferUnknownIdentifierFallsBackToDeclaration(t *testing.T) {
	v := newLoadedValidator(t)
	data := append([]byte(nil), realOMM(t)...)
	copy(data[8:12], []byte("$ZZZ"))
	decision, err := v.RouteBuffer("OMM.fbs", data)
	if err != nil {
		t.Fatalf("RouteBuffer: %v", err)
	}
	if decision.FromHeader {
		t.Fatal("an unknown identifier must not be treated as a route")
	}
	if decision.Schema != "OMM.fbs" {
		t.Fatalf("fallback schema = %q, want the declaration OMM.fbs", decision.Schema)
	}
	if err := v.VerifyEnvelope(decision.Schema, data); err == nil {
		t.Fatal("VerifyEnvelope must still refuse a buffer carrying the wrong identifier")
	}
}

func TestRouteBufferRefusesWhenNothingIdentifiesTheRecord(t *testing.T) {
	v := newLoadedValidator(t)
	if _, err := v.RouteBuffer("", []byte{0, 0, 0}); err == nil {
		t.Fatal("no declaration and no header must be an error, not a guess")
	}
}

// Every embedded schema that declares an identifier must be reachable from it,
// and no identifier may be claimed twice — the reverse index is only sound if
// that holds on the real corpus.
func TestIdentifierIndexIsCompleteAndUnambiguous(t *testing.T) {
	v := newLoadedValidator(t)
	v.mu.RLock()
	forward := make(map[string]string, len(v.identifiers))
	for schema, ident := range v.identifiers {
		forward[schema] = ident
	}
	ambiguous := len(v.ambiguousIdents)
	v.mu.RUnlock()

	if len(forward) < 150 {
		t.Fatalf("only %d schemas declare an identifier; the corpus did not load", len(forward))
	}
	if ambiguous != 0 {
		t.Fatalf("%d file identifiers are claimed by more than one schema", ambiguous)
	}
	for schema, ident := range forward {
		got, ok := v.SchemaForIdentifier(ident)
		if !ok || got != schema {
			t.Fatalf("SchemaForIdentifier(%q) = %q ok=%v, want %q", ident, got, ok, schema)
		}
	}
}

package sds

import (
	"context"
	"encoding/binary"
	"strings"
	"testing"
)

// newNoWASMValidator builds a validator with no flatc module — the state of every
// packaged deployment, since findWasmPath() only probes build-tree paths that do
// not exist there. Validation MUST still be real in this configuration.
func newNoWASMValidator(t *testing.T) *Validator {
	t.Helper()
	v, err := NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	return v
}

func validOMM(t *testing.T) []byte {
	t.Helper()
	return NewOMMBuilder().
		WithObjectName("ISS (ZARYA)").
		WithObjectID("1998-067A").
		WithNoradCatID(25544).
		WithEpoch("2026-07-14T00:00:00.000000Z").
		WithMeanMotion(15.5).
		WithEccentricity(0.0004).
		WithInclination(51.6).
		Build()
}

func TestValidatorRejectsJunkWithoutWASM(t *testing.T) {
	v := newNoWASMValidator(t)
	ctx := context.Background()

	cases := []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"single junk byte", []byte("X")},
		{"short junk", []byte("JUNKJUNKJUNK")},
		{"json payload", []byte(`{"NORAD_CAT_ID":25544}`)},
		{"root offset past end", []byte{0xff, 0xff, 0xff, 0x7f, 0, 0, 0, 0, 0, 0, 0, 0}},
		{"plausible length, garbage body", make([]byte, 64)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := v.Validate(ctx, "OMM.fbs", tc.data); err == nil {
				t.Fatalf("Validate accepted %d junk bytes as a valid OMM record", len(tc.data))
			}
		})
	}
}

func TestValidatorAcceptsRealOMMWithoutWASM(t *testing.T) {
	v := newNoWASMValidator(t)
	if err := v.Validate(context.Background(), "OMM.fbs", validOMM(t)); err != nil {
		t.Fatalf("Validate rejected a real size-prefixed OMM record: %v", err)
	}
}

// A record must carry its OWN schema's identifier: an OMM buffer offered as an OCM
// is a schema confusion, not a valid publish.
func TestValidatorRejectsWrongSchemaIdentifier(t *testing.T) {
	v := newNoWASMValidator(t)
	err := v.Validate(context.Background(), "OCM.fbs", validOMM(t))
	if err == nil {
		t.Fatal("Validate accepted an OMM buffer as an OCM record")
	}
	if !strings.Contains(err.Error(), "$OCM") {
		t.Fatalf("error should name the expected identifier, got: %v", err)
	}
}

func TestValidatorRejectsTruncatedRecord(t *testing.T) {
	v := newNoWASMValidator(t)
	full := validOMM(t)

	for _, n := range []int{1, 4, 8, 12, len(full) / 2, len(full) - 1} {
		if err := v.Validate(context.Background(), "OMM.fbs", full[:n]); err == nil {
			t.Fatalf("Validate accepted a %d-byte truncation of a %d-byte OMM record", n, len(full))
		}
	}
}

// The store only decodes records whose size prefix is intact
// (OMM.SizePrefixedOMMBufferHasIdentifier), so a wrong prefix must not publish.
func TestValidatorRejectsBadSizePrefix(t *testing.T) {
	v := newNoWASMValidator(t)
	buf := validOMM(t)
	binary.LittleEndian.PutUint32(buf[:4], uint32(len(buf)+99))

	if err := v.Validate(context.Background(), "OMM.fbs", buf); err == nil {
		t.Fatal("Validate accepted an OMM record with a corrupt size prefix")
	}
}

func TestValidatorUnknownSchemaRejected(t *testing.T) {
	v := newNoWASMValidator(t)
	if err := v.Validate(context.Background(), "NOPE.fbs", validOMM(t)); err == nil {
		t.Fatal("Validate accepted a record for an unknown schema")
	}
}

func TestValidatorLoadsFileIdentifiers(t *testing.T) {
	v := newNoWASMValidator(t)

	for schema, want := range map[string]string{
		"OMM.fbs": "$OMM",
		"OCM.fbs": "$OCM",
		"CDM.fbs": "$CDM",
	} {
		got, ok := v.FileIdentifier(schema)
		if !ok {
			t.Fatalf("no file identifier recorded for %s", schema)
		}
		if got != want {
			t.Fatalf("FileIdentifier(%s) = %q, want %q", schema, got, want)
		}
	}
}

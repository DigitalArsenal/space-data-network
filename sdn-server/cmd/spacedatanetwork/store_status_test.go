package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/encfield"
)

func TestStoreStatusIsReadOnly(t *testing.T) {
	root := t.TempDir()
	var out bytes.Buffer
	if err := writeStoreStatus(&out, root); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "store_backend_format=1\n") || !strings.Contains(out.String(), "store_layout=0\n") {
		t.Fatal(out.String())
	}
	// The embedded standards: none is left unregistered (KMF is sealed, but
	// no extraction rule reads its sealed field).
	if !strings.Contains(out.String(), "store_unregistered_standards=none\n") {
		t.Fatal(out.String())
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("status changed store: %v %v", entries, err)
	}
}

// A standard whose sealed field an extraction rule reads is named, with the
// rule, where an operator reads the store's status.
func TestStoreStatusNamesUnregisteredStandards(t *testing.T) {
	encfield.RegisterSchema("PNM", []encfield.FieldSpec{{Name: "FILE_ID", FieldID: 4}})
	t.Cleanup(func() { encfield.RegisterSchema("PNM", nil) })
	var out bytes.Buffer
	if err := writeStoreStatus(&out, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "store_unregistered_standards=PNM.fbs\n") ||
		!strings.Contains(out.String(), "store_unregistered_standard=PNM.fbs: a field sealed at rest is an extraction-rule field: PNM.FILE_ID") {
		t.Fatal(out.String())
	}
}

package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
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
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("status changed store: %v %v", entries, err)
	}
}

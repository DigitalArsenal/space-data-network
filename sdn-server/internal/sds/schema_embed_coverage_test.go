package sds

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestDriftGuardCoverageIsDeclared closes the hole that let RFB.fbs drift for
// weeks and then let CES.fbs drift again (sdn-rec-ordinal-hardcoded-mbl-80).
//
// TestEmbeddedSchemasMatchLinkedBindings is a good guard with a bad denominator:
// it only checks the schemas somebody remembered to add to driftGuardedSchemas.
// Every embed outside that hand-list is unguarded, and nothing says which those
// are — so "we forgot one" is invisible until a field silently fails to decode.
//
// This test makes the omission LOUD instead. Every embedded schema is either
// drift-guarded or explicitly listed below as knowingly unguarded. Adding an
// embed without deciding which bucket it belongs in fails the build.
//
// Guarding a schema costs one line in driftGuardedSchemas and requires only that
// the pinned Go bindings expose the type. That is the preferred bucket; the
// waiver exists so this test states the truth rather than forcing 190 typed
// instances the node never decodes.
func TestDriftGuardCoverageIsDeclared(t *testing.T) {
	guarded := map[string]bool{}
	for _, g := range driftGuardedSchemas {
		guarded[g.file] = true
	}

	entries, err := os.ReadDir("schemas")
	if err != nil {
		t.Fatalf("read embedded schema dir: %v", err)
	}

	var undeclared []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".fbs") {
			continue
		}
		if guarded[name] || unguardedEmbeddedSchemas[name] {
			continue
		}
		undeclared = append(undeclared, name)
	}

	if len(undeclared) > 0 {
		sort.Strings(undeclared)
		t.Errorf("%d embedded schema(s) are neither drift-guarded nor declared unguarded: %v\n"+
			"Add each to driftGuardedSchemas (preferred — one line, and it can never drift unnoticed) "+
			"or to unguardedEmbeddedSchemas with the reason. An embed the node decodes MUST be guarded.",
			len(undeclared), undeclared)
	}

	// The waiver list must not rot either: a waived file that no longer exists,
	// or that has since been guarded, is a stale declaration.
	present := map[string]bool{}
	for _, entry := range entries {
		present[entry.Name()] = true
	}
	for name := range unguardedEmbeddedSchemas {
		if !present[name] {
			t.Errorf("unguardedEmbeddedSchemas lists %s, which is not embedded — remove the stale waiver", name)
		}
		if guarded[name] {
			t.Errorf("%s is both drift-guarded and waived — drop the waiver", name)
		}
	}
}

// TestEmbeddedSchemasAreReachable is a cheap sanity check that the embed
// directive and the directory agree, so a schema cannot be added to the tree and
// silently left out of the binary (or vice versa).
func TestEmbeddedSchemasAreReachable(t *testing.T) {
	entries, err := os.ReadDir("schemas")
	if err != nil {
		t.Fatalf("read embedded schema dir: %v", err)
	}
	checked := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".fbs") {
			continue
		}
		if _, err := schemasFS.ReadFile(filepath.Join("schemas", entry.Name())); err != nil {
			t.Errorf("%s is on disk but not readable through the embed: %v", entry.Name(), err)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no embedded schemas found — the embed directive or the directory moved")
	}
	t.Logf("%d embedded schemas reachable; %d drift-guarded, %d explicitly unguarded",
		checked, len(driftGuardedSchemas), len(unguardedEmbeddedSchemas))
}

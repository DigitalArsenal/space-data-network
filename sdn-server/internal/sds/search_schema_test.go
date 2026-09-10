package sds

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestSearchSchemasMatchGeneratedInputsAndOutputs(t *testing.T) {
	data, err := os.ReadFile("search-schemas/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		SDSVersion string            `json:"sdsVersion"`
		Inputs     map[string]string `json:"inputs"`
		Outputs    map[string]string `json:"outputs"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Outputs) < 200 {
		t.Fatal("incomplete binary schema catalog")
	}
	goMod, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	version := regexp.MustCompile(`github\.com/DigitalArsenal/spacedatastandards\.org/lib/go v([^\s]+)`).FindSubmatch(goMod)
	if len(version) != 2 || string(version[1]) != manifest.SDSVersion {
		t.Fatal("regenerate search schemas after updating the pinned SDS dependency")
	}
	for path, expected := range manifest.Inputs {
		if !strings.HasPrefix(path, "embedded/") {
			continue
		}
		bytes, err := schemasFS.ReadFile("schemas/" + strings.TrimPrefix(path, "embedded/"))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(bytes)
		if hex.EncodeToString(sum[:]) != expected {
			t.Fatalf("regenerate search schema after changing %s", path)
		}
	}
	for name, expected := range manifest.Outputs {
		bytes, ok := SearchSchema(strings.TrimSuffix(name, ".bfbs"))
		if !ok || len(bytes) < 8 || string(bytes[4:8]) != "BFBS" {
			t.Fatalf("missing binary schema %s", name)
		}
		sum := sha256.Sum256(bytes)
		if hex.EncodeToString(sum[:]) != expected {
			t.Fatalf("binary schema differs from manifest: %s", name)
		}
	}
	for _, name := range []string{"NCD", "ncd.fbs", " NCD.FBS "} {
		if _, ok := SearchSchema(name); !ok {
			t.Fatalf("unrecognized canonical spelling %q", name)
		}
	}
	for _, name := range []string{"", "../NCD", "NCD.bfbs", "NCD/other", "UNKNOWN"} {
		if _, ok := SearchSchema(name); ok {
			t.Fatalf("unexpected schema path accepted: %q", name)
		}
	}
}

package wasmrt_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"github.com/spacedatanetwork/sdn-server/internal/flowrt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
)

// This is deliberately the production module loader, not the SDK test host.
// CI/deployment supplies hash-pinned portable artifacts and independently built
// canonical FlatBuffer requests. No network or numerical algorithm lives here.
func TestScienceArtifactsInProductionHost(t *testing.T) {
	root := os.Getenv("SDN_SCIENCE_FIXTURES")
	if root == "" {
		t.Skip("set SDN_SCIENCE_FIXTURES for the required science deployment gate")
	}
	var cases []struct {
		Name, Artifact, SHA256, Method, Request, Output, Schema string
		Pages                                                   uint32
	}
	bytes, err := os.ReadFile(filepath.Join(root, "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(bytes, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("empty science gate")
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			bytes, err := os.ReadFile(c.Artifact)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(bytes)
			if hex.EncodeToString(digest[:]) != c.SHA256 {
				t.Fatal("artifact hash mismatch")
			}
			budgets, _ := json.Marshal(map[string]uint32{c.SHA256: c.Pages})
			t.Setenv("SDN_WASM_MEMORY_BUDGETS", string(budgets))
			module, err := modulert.NewModule(bytes, modulert.NewCapabilityRegistry(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer module.Close()
			request, err := os.ReadFile(filepath.Join(root, c.Request))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			response, err := module.InvokeMethodFrames(ctx, c.Method, []modulert.InvokeInputFrame{{PortID: "request", Payload: request, SchemaName: c.Schema + ".fbs", FileIdentifier: "$" + c.Schema, RootTypeName: c.Schema}})
			if err != nil {
				t.Fatal(err)
			}
			if len(response) < 8 {
				t.Fatal("empty or malformed response")
			}
			if err = os.WriteFile(filepath.Join(root, c.Output), response, 0600); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMemoryBudgetIsPerArtifact(t *testing.T) {
	// Independent WebAssembly fixture: exported memory with initial 2 pages.
	bytes, _ := hex.DecodeString("0061736d010000000503010002070a01066d656d6f72790200")
	t.Setenv("SDN_WASM_MEMORY_BUDGETS", "")
	if m, err := wasmrt.NewModule(bytes, wasmrt.WithMaxMemoryPages(1)); err == nil {
		m.Release()
		t.Fatal("accepted memory above budget")
	}
	digest := sha256.Sum256(bytes)
	grants, _ := json.Marshal(map[string]uint32{hex.EncodeToString(digest[:]): 2})
	t.Setenv("SDN_WASM_MEMORY_BUDGETS", string(grants))
	m, err := wasmrt.NewModule(bytes, wasmrt.WithMaxMemoryPages(1))
	if err != nil {
		t.Fatal(err)
	}
	m.Release()
	t.Setenv("SDN_WASM_MEMORY_BUDGETS", `{"invalid":2}`)
	if m, err := wasmrt.NewModule(bytes); err == nil {
		m.Release()
		t.Fatal("accepted invalid operator policy")
	}
}

func TestHiddenMemoryBudget(t *testing.T) {
	bytes, _ := hex.DecodeString("0061736d010000000503010002")
	t.Setenv("SDN_WASM_MEMORY_BUDGETS", "")
	if m, err := wasmrt.NewModule(bytes, wasmrt.WithMaxMemoryPages(1)); err == nil {
		m.Release()
		t.Fatal("accepted unexported memory above budget")
	}
	t.Setenv("SDN_WASM_MEMORY_BUDGETS", `{"0000000000000000000000000000000000000000000000000000000000000000":2}`)
	if m, err := wasmrt.NewModule(bytes, wasmrt.WithMaxMemoryPages(1)); err == nil {
		m.Release()
		t.Fatal("unrelated artifact inherited another budget")
	}
}

// Exercise the same HTTP mount and flow runtime used by the daemon, including
// framing. The independent numerical checker decodes these outputs against the
// captured SOCRATES edition; successful HTTP status alone is not parity.
func TestScienceHTTPFlow(t *testing.T) {
	dist := os.Getenv("SDN_SCIENCE_FLOW")
	root := os.Getenv("SDN_SCIENCE_FIXTURES")
	if dist == "" || root == "" {
		t.Skip("set SDN_SCIENCE_FLOW and SDN_SCIENCE_FIXTURES")
	}
	m, err := flowrt.LoadMountedFlow(dist, flowrt.FlowMountDeps{CapRegistry: modulert.NewCapabilityRegistry(), MaxMemoryPages: 8192, PoolSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	for _, method := range []string{"GET", "POST"} {
		request, err := os.ReadFile(filepath.Join(root, "socrates-1.fb"))
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		m.ServeHTTP(rec, httptest.NewRequest(method, "/api/v1/science/screen", bytes.NewReader(request)))
		if method == "GET" {
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("GET: %d: %s", rec.Code, rec.Body.String())
			}
			continue
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("POST: %d: %s", rec.Code, rec.Body.String())
		}
		response := rec.Body.Bytes()
		if len(response) < 12 {
			t.Fatalf("short response: %q", response)
		}
		size := int(binary.LittleEndian.Uint32(response))
		if size < 8 || size > len(response)-4 {
			t.Fatal("invalid CQR stream framing")
		}
		if string(response[8:12]) != "$CQR" {
			t.Fatal("wrong result schema")
		}
		if err := os.WriteFile(filepath.Join(root, "result-flow-socrates-1.fb"), response[4:4+size], 0600); err != nil {
			t.Fatal(err)
		}
	}
}

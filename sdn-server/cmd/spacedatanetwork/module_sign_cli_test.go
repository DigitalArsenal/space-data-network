package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/modulesign"
	"github.com/spf13/cobra"
)

func moduleCLIReceipt(t *testing.T, artifact []byte) moduleSignResponse {
	t.Helper()
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{42}, ed25519.SeedSize))
	signer, err := modulesign.NewSigner(key, "test-publisher", modulesign.NewAuditLog(filepath.Join(t.TempDir(), "audit.jsonl")))
	if err != nil {
		t.Fatal(err)
	}
	result, err := signer.Sign(modulesign.Request{Artifact: artifact, Requester: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return moduleSignResponse{
		ContentHash: result.ContentHash, StatementDomain: result.StatementDomain, Algorithm: result.Algorithm,
		PublicKeyHex: result.PublicKeyHex, SignatureHex: result.SignatureHex, PortableBytes: result.PortableBytes,
		TrailerStripped: result.TrailerStripped, SignedAt: result.SignedAt.Format("2006-01-02T15:04:05Z07:00"), SignatureEntry: result.Entry,
	}
}

func TestModuleSignCLITransmitsRawAuthenticatedArtifactAndWritesVerifiedReceipt(t *testing.T) {
	artifact := []byte{0, 'a', 's', 'm', 1, 0, 0, 0}
	receipt := moduleCLIReceipt(t, artifact)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != "POST" || r.URL.Path != "/api/v1/admin/modules/sign" || r.URL.RawQuery != "" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || cookie.Value != "test-session" {
			t.Error("missing native session cookie")
		}
		if r.Header.Get("Content-Type") != "application/wasm" || r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
			t.Error("missing raw-WASM/CSRF headers")
		}
		for name := range r.Header {
			if strings.Contains(strings.ToLower(name), "hash") || strings.Contains(strings.ToLower(name), "digest") {
				t.Errorf("caller digest header: %s", name)
			}
		}
		body, _ := io.ReadAll(r.Body)
		if !bytes.Equal(body, artifact) {
			t.Errorf("body changed: %x", body)
		}
		json.NewEncoder(w).Encode(receipt)
	}))
	defer server.Close()
	dir := t.TempDir()
	input, output := filepath.Join(dir, "module.wasm"), filepath.Join(dir, "receipt.json")
	if err := os.WriteFile(input, artifact, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := newModuleSignCommand(func(*cobra.Command) (*adminClient, error) {
		return &adminClient{baseURL: server.URL, http: server.Client(), token: "test-session"}, nil
	})
	cmd.SetOut(io.Discard)
	cmd.SetArgs([]string{"--wasm", input, "--out", output, "--trusted-public-key", receipt.PublicKeyHex})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var got moduleSignResponse
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if got.SignatureEntry != receipt.SignatureEntry {
		t.Fatal("SDK signature entry changed")
	}
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing output should be preserved: %v", err)
	}
	if requests != 1 {
		t.Fatalf("unexpected signing calls: %d", requests)
	}
}

func TestModuleSignCLIRefusesUntrustedOrMismatchedReceipt(t *testing.T) {
	artifact := []byte{0, 'a', 's', 'm', 1, 0, 0, 0}
	original := moduleCLIReceipt(t, artifact)
	cases := map[string]func(*moduleSignResponse){
		"hash":                 func(r *moduleSignResponse) { r.ContentHash = strings.Repeat("0", 64) },
		"domain":               func(r *moduleSignResponse) { r.StatementDomain = "SDN-UPDATE-MANIFEST-V1" },
		"algorithm":            func(r *moduleSignResponse) { r.Algorithm = "other" },
		"entry hash algorithm": func(r *moduleSignResponse) { r.SignatureEntry.SignedHashAlgorithm = "sha256" },
		"entry domain":         func(r *moduleSignResponse) { r.SignatureEntry.StatementDomain = "" },
		"entry signature":      func(r *moduleSignResponse) { r.SignatureEntry.SignatureHex = strings.Repeat("0", 128) },
		"different publisher": func(r *moduleSignResponse) {
			r.PublicKeyHex = strings.Repeat("0", 64)
			r.SignatureEntry.PublicKeyHex = r.PublicKeyHex
		},
		"invalid signature": func(r *moduleSignResponse) {
			r.SignatureHex = strings.Repeat("0", 128)
			r.SignatureEntry.SignatureHex = r.SignatureHex
		},
		"portable bytes": func(r *moduleSignResponse) { r.PortableBytes++ },
		"trailer status": func(r *moduleSignResponse) { r.TrailerStripped = true },
	}
	if err := verifyModuleSignResponse(artifact, original.PublicKeyHex, &original); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			changed := original
			mutate(&changed)
			if verifyModuleSignResponse(artifact, original.PublicKeyHex, &changed) == nil {
				t.Fatal("invalid receipt accepted")
			}
		})
	}
	if verifyModuleSignResponse(append(artifact, 0), original.PublicKeyHex, &original) == nil {
		t.Fatal("signature accepted for another module")
	}
	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{43}, 32)).Public().(ed25519.PublicKey)
	if verifyModuleSignResponse(artifact, hex.EncodeToString(other), &original) == nil {
		t.Fatal("unexpected signer accepted")
	}
}

func TestModuleSignCLINoReceiptOnAuthenticationOrVerificationFailure(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusOK} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.WriteHeader(status)
				io.WriteString(w, `{}`)
			}))
			defer server.Close()
			dir := t.TempDir()
			input, output := filepath.Join(dir, "module.wasm"), filepath.Join(dir, "receipt.json")
			os.WriteFile(input, []byte{0, 'a', 's', 'm', 1, 0, 0, 0}, 0o600)
			cmd := newModuleSignCommand(func(*cobra.Command) (*adminClient, error) {
				return &adminClient{baseURL: server.URL, http: server.Client(), token: "test-session"}, nil
			})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"--wasm", input, "--out", output, "--trusted-public-key", strings.Repeat("0", 64)})
			if err := cmd.Execute(); err == nil {
				t.Fatal("expected failure")
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatal("failed request wrote a receipt")
			}
			if requests != 1 {
				t.Fatalf("unexpected retry/anonymous fallback: %d", requests)
			}
		})
	}
}

func TestModuleSignCLIReceiptIsCompleteAndExclusive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "receipt.json")
	first, second := []byte(`{"receipt":"first"}`), []byte(`{"receipt":"second"}`)
	if err := writeModuleSignatureReceipt(path, first); err != nil {
		t.Fatal(err)
	}
	if err := writeModuleSignatureReceipt(path, second); err == nil {
		t.Fatal("overwrote existing receipt")
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, first) {
		t.Fatalf("original receipt changed: %q, %v", got, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary receipt leaked: %v, %v", entries, err)
	}
	missing := filepath.Join(dir, "missing", "receipt.json")
	if err := writeModuleSignatureReceipt(missing, second); err == nil {
		t.Fatal("write unexpectedly succeeded")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("failed write exposed a receipt")
	}
}

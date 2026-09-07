package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestCapabilityPolicyCLIUsesAuthenticatedNativeRoutes(t *testing.T) {
	hash := strings.Repeat("a", 64)
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || cookie.Value != "test-session" {
			t.Error("missing native session")
		}
		if r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
			t.Error("missing CSRF header")
		}
		if r.Method == "GET" {
			io.WriteString(w, `[]`)
			return
		}
		var approval capabilityApprovalRequest
		if err := json.NewDecoder(r.Body).Decode(&approval); err != nil {
			t.Fatal(err)
		}
		if approval.ModuleHash != hash || approval.Capability != "http" || approval.PluginID != "test.module" || approval.Note != "same declared capability" {
			t.Errorf("unexpected approval: %+v", approval)
		}
		json.NewEncoder(w).Encode(approval)
	}))
	defer server.Close()
	connect := func(*cobra.Command) (*adminClient, error) {
		return &adminClient{baseURL: server.URL, http: server.Client(), token: "test-session"}, nil
	}
	for _, args := range [][]string{
		{"list", "--module-hash", strings.ToUpper(hash)},
		{"approve", "--module-hash", hash, "--capability", "http", "--plugin-id", "test.module", "--note", "same declared capability"},
	} {
		cmd := newCapabilityPolicyCommand(connect)
		cmd.SetArgs(args)
		var out bytes.Buffer
		cmd.SetOut(&out)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		if !json.Valid(out.Bytes()) {
			t.Fatalf("invalid output: %s", out.String())
		}
	}
	want := []string{"GET /api/modules/capabilities?module_hash=" + hash, "POST /api/modules/capabilities/approve"}
	if strings.Join(requests, "\n") != strings.Join(want, "\n") {
		t.Fatalf("unexpected native routes: %v", requests)
	}
}

func TestCapabilityPolicyCLIValidationPrecedesAuthentication(t *testing.T) {
	for _, args := range [][]string{
		{"approve", "--module-hash", "bad", "--capability", "http"},
		{"approve", "--module-hash", strings.Repeat("a", 64)},
		{"approve", "--module-hash", strings.Repeat("a", 64), "--capability", "http", "--signer-public-key", "bad"},
		{"list", "--module-hash", "bad"},
	} {
		cmd := newCapabilityPolicyCommand(func(*cobra.Command) (*adminClient, error) {
			t.Fatal("invalid input reached authentication")
			return nil, nil
		})
		cmd.SetArgs(args)
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		if cmd.Execute() == nil {
			t.Fatalf("accepted invalid arguments: %v", args)
		}
	}
}

func TestCapabilityPolicyCLIRefusesAuthFailureAndWrongAcknowledgement(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusOK} {
		requests := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests++
			w.WriteHeader(status)
			io.WriteString(w, `{"module_hash":"wrong","capability":"http"}`)
		}))
		cmd := newCapabilityPolicyCommand(func(*cobra.Command) (*adminClient, error) {
			return &adminClient{baseURL: server.URL, http: server.Client(), token: "test-session"}, nil
		})
		cmd.SetArgs([]string{"approve", "--module-hash", strings.Repeat("a", 64), "--capability", "http"})
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		if cmd.Execute() == nil {
			t.Fatal("unconfirmed approval accepted")
		}
		server.Close()
		if requests != 1 {
			t.Fatalf("unexpected retry/anonymous fallback: %d", requests)
		}
	}
}

func TestCapabilityPolicyCLIRequiresRequestedIdentityAcknowledgement(t *testing.T) {
	for _, field := range []string{"plugin_id", "signer_pubkey_hex"} {
		t.Run(field, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request map[string]string
				json.NewDecoder(r.Body).Decode(&request)
				delete(request, field)
				json.NewEncoder(w).Encode(request)
			}))
			defer server.Close()
			cmd := newCapabilityPolicyCommand(func(*cobra.Command) (*adminClient, error) {
				return &adminClient{baseURL: server.URL, http: server.Client(), token: "test-session"}, nil
			})
			cmd.SetArgs([]string{"approve", "--module-hash", strings.Repeat("a", 64), "--capability", "http", "--plugin-id", "test.module", "--signer-public-key", strings.Repeat("b", 64)})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			if cmd.Execute() == nil {
				t.Fatal("missing requested identity acknowledgement accepted")
			}
		})
	}
}

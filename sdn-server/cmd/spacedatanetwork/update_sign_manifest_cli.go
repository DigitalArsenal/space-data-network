package main

// `spacedatanetwork update sign-manifest` — the producer's client half of the
// content-bound update-manifest signing endpoint.
//
// The release is built LOCALLY and the publisher key lives ONLY on the node, so
// the manifest travels to the key and comes back with a detached signature. See
// internal/updatesign for the node half and docs/sdn-signed-updater.md for the
// ruling.
//
// WHY A CLI AND NOT A SHELL SCRIPT IN deployment/. Two things a script would
// have to reimplement badly: the §19 admit-point ceremony (challenge, sign the
// raw 32 bytes with the node root key, exchange for a session cookie) and the
// daemon's own TLS anchor. adminClient already does both, correctly, and is the
// one place the CLI is allowed to obtain Admin authority.
//
// WHERE IT RUNS, and the honest limitation. With no --session-token it signs in
// with the NODE'S ROOT KEY, which means it must run where the node's seed is
// readable — i.e. on the publisher host. A build box signs in with
// --session-token / $SDN_SESSION_TOKEN instead. WHICH principal a build box
// should present is an OWNER decision that has not been taken: the only admin
// principal on host-01 today is the OrbPro Pages Publisher, whose private half
// sits in a CI secret, and Hephaestus has recorded that he does not concur to
// that as the shipped caller on a bonded key. Until a purpose-scoped signing
// principal is issued, the supported lane is: run this on the publisher host
// with the manifest file, carry the signed manifest back.
//
// The registration lives in this file's own init() rather than in main.go's
// dispatch block, which is under another task's claim.

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/sigdomain"
	"github.com/spacedatanetwork/sdn-server/internal/update"
)

var (
	updateSignManifestIn      string
	updateSignManifestOut     string
	updateSignManifestNodeURL string
)

// updateSignManifestResponse mirrors internal/api.updateManifestSignResponse.
type updateSignManifestResponse struct {
	ContentHash     string `json:"content_hash"`
	StatementDomain string `json:"statement_domain"`
	Algorithm       string `json:"algorithm"`
	KeyID           string `json:"key_id"`
	PublicKey       string `json:"public_key"`
	PublicKeyHex    string `json:"public_key_hex"`
	Signature       string `json:"signature"`
	CanonicalBytes  int    `json:"canonical_bytes"`
	SignedAt        string `json:"signed_at"`
	UpdateID        string `json:"update_id"`
	Version         string `json:"version"`
	Sequence        int64  `json:"sequence"`
	Channel         string `json:"channel"`
	Target          string `json:"target"`
}

var updateSignManifestCmd = &cobra.Command{
	Use:   "sign-manifest",
	Short: "Sign an update manifest with this node's publisher key",
	Long: "Submits an unsigned update manifest to the node's content-bound signing endpoint. " +
		"The node canonicalizes and hashes the document itself and returns a detached Ed25519 " +
		"signature over a domain-separated statement; the signed manifest is written to --out. " +
		"The signing key never leaves the node.",
	RunE: func(cmd *cobra.Command, _ []string) error {
		if strings.TrimSpace(updateSignManifestIn) == "" {
			return errors.New("--manifest is required")
		}
		if strings.TrimSpace(updateSignManifestOut) == "" {
			return errors.New("--out is required")
		}

		raw, err := os.ReadFile(updateSignManifestIn)
		if err != nil {
			return fmt.Errorf("read manifest: %w", err)
		}

		// Set the statement domain HERE, before submission, because it is part
		// of the signed document. Doing it after the fact would be an unsigned
		// assertion about a signed manifest.
		var doc map[string]any
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.UseNumber()
		if err := decoder.Decode(&doc); err != nil {
			return fmt.Errorf("parse manifest: %w", err)
		}
		signing, _ := doc["signing"].(map[string]any)
		if signing == nil {
			return errors.New("manifest has no signing block")
		}
		signing["statement_domain"] = sigdomain.DomainUpdateManifestV1
		delete(signing, "signature")

		submitted, err := json.Marshal(doc)
		if err != nil {
			return fmt.Errorf("encode manifest: %w", err)
		}

		client, err := newUpdateSigningClient(cmd)
		if err != nil {
			return err
		}

		var result updateSignManifestResponse
		if err := client.postJSON(context.Background(), "/api/v1/admin/updates/sign-manifest",
			json.RawMessage(submitted), &result, ""); err != nil {
			return err
		}
		if result.StatementDomain != sigdomain.DomainUpdateManifestV1 {
			return fmt.Errorf("node signed under an unexpected statement domain %q", result.StatementDomain)
		}
		if strings.TrimSpace(result.Signature) == "" {
			return errors.New("node returned no signature")
		}

		signing["signature"] = result.Signature
		signed, err := json.Marshal(doc)
		if err != nil {
			return fmt.Errorf("encode signed manifest: %w", err)
		}

		// VERIFY BEFORE WRITING. Publishing a manifest nobody can verify is,
		// from the fleet's side, indistinguishable from an outage — and it would
		// be discovered one box at a time. Re-deriving the statement locally
		// also catches a canonicalization disagreement between this build and
		// the node's, which is the failure this whole design is arranged to make
		// impossible.
		if err := verifyNodeSignedManifest(signed, result.PublicKey); err != nil {
			return err
		}

		if dir := filepath.Dir(updateSignManifestOut); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("create output directory: %w", err)
			}
		}
		if err := os.WriteFile(updateSignManifestOut, append(signed, '\n'), 0o644); err != nil {
			return fmt.Errorf("write signed manifest: %w", err)
		}

		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "signed_manifest=%s\n", updateSignManifestOut)
		fmt.Fprintf(out, "statement_domain=%s\n", result.StatementDomain)
		fmt.Fprintf(out, "content_hash=%s\n", result.ContentHash)
		fmt.Fprintf(out, "key_id=%s\n", result.KeyID)
		fmt.Fprintf(out, "public_key=%s\n", result.PublicKey)
		fmt.Fprintf(out, "update_id=%s version=%s sequence=%d channel=%s target=%s\n",
			result.UpdateID, result.Version, result.Sequence, result.Channel, result.Target)
		fmt.Fprintf(out, "verified_locally=yes\n")
		fmt.Fprintf(out, "trust_root_entry={%q: %q}\n", result.KeyID, result.PublicKey)
		return nil
	},
}

// newUpdateSigningClient is newAdminClient with an explicit destination.
//
// WHY THIS EXISTS. newAdminClient derives its base URL from the config's BIND
// address (adminURL, main.go), mapping a wildcard bind to loopback. On the
// publisher host that yields https://127.0.0.1:443 — and host-01's admin
// listener is 0.0.0.0:443 fronted by a REAL certificate for sdn.spaceaware.io.
// No certificate can be valid for 127.0.0.1 there, so the ceremony fails at TLS
// verification before it can ask for a challenge. That is not a misconfigured
// node; it is the correct shape for a node that also serves the public web, and
// the CLI simply had no way to say "dial me by the name on the certificate".
//
// --node-url says it. The seed still has to be local (the ceremony signs with
// the node's root key), so this does not widen who may sign; it only fixes how
// the local operator reaches the local daemon.
func newUpdateSigningClient(cmd *cobra.Command) (*adminClient, error) {
	nodeURL := strings.TrimSpace(updateSignManifestNodeURL)
	if nodeURL == "" {
		return newAdminClient(cmd)
	}

	cfg, res, err := config.LoadResolved(configPath)
	if err != nil {
		return nil, err
	}
	c := &adminClient{
		baseURL: strings.TrimRight(nodeURL, "/"),
		http:    &http.Client{Timeout: 60 * time.Second},
		cfg:     cfg,
		res:     res,
	}

	// An operator-supplied token means we are not expected to hold the seed.
	if token := sessionTokenOverride(cmd); token != "" {
		c.token = token
		return c, nil
	}

	// Verification stays on SYSTEM roots: a public hostname implies a publicly
	// issued certificate, and pinning the node's own self-signed anchor here
	// would be the wrong trust store for the name being dialled.
	token, err := c.signInWithNodeRootKey(context.Background())
	if err != nil {
		return nil, err
	}
	c.token = token
	return c, nil
}

// verifyNodeSignedManifest re-derives the domain statement and checks the
// signature against the key the node reported.
func verifyNodeSignedManifest(signed []byte, publicKeyB64 string) error {
	if strings.TrimSpace(publicKeyB64) == "" {
		return errors.New("node returned no public key to verify against")
	}
	der, err := base64.StdEncoding.DecodeString(publicKeyB64)
	if err != nil {
		return fmt.Errorf("decode node public key: %w", err)
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return fmt.Errorf("parse node public key: %w", err)
	}
	pub, ok := parsed.(ed25519.PublicKey)
	if !ok {
		return errors.New("node public key is not Ed25519")
	}

	canonical, err := update.CanonicalManifestBytes(signed)
	if err != nil {
		return fmt.Errorf("canonicalize signed manifest: %w", err)
	}
	sum := sha256.Sum256(canonical)
	statement, err := sigdomain.Statement(sigdomain.DomainUpdateManifestV1, sum[:])
	if err != nil {
		return err
	}

	var manifest struct {
		Signing struct {
			Signature string `json:"signature"`
		} `json:"signing"`
	}
	if err := json.Unmarshal(signed, &manifest); err != nil {
		return fmt.Errorf("read back signature: %w", err)
	}
	sig, err := base64.StdEncoding.DecodeString(manifest.Signing.Signature)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}
	if !ed25519.Verify(pub, statement, sig) {
		return errors.New(
			"the signature the node returned does not verify over the manifest that was submitted — " +
				"producer and node disagree about the canonical bytes; this release must NOT be published")
	}
	return nil
}

func init() {
	updateSignManifestCmd.Flags().StringVar(&updateSignManifestIn, "manifest", "", "path of the unsigned manifest.json to sign")
	updateSignManifestCmd.Flags().StringVar(&updateSignManifestOut, "out", "", "path to write the signed manifest.json")
	updateSignManifestCmd.Flags().StringVar(&updateSignManifestNodeURL, "node-url", "",
		"explicit base URL of the signing node (default: derived from the config bind address; set this when the daemon's certificate is issued for a public hostname rather than loopback)")
	// Declared with no bound variable: sessionTokenOverride reads it back off
	// the command's own flag set (admin_client.go), so every command that wants
	// the remote-target override just has to DEFINE the flag.
	updateSignManifestCmd.Flags().String("session-token", "",
		"session token for the admin API (default: $SDN_SESSION_TOKEN; omit to sign in with the node's root key)")
	updateCmd.AddCommand(updateSignManifestCmd)
}

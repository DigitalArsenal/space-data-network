package main

// This is the client half of the existing content-bound module signer. The
// native admin client keeps the root key and wallet session on the node; only
// the verified public receipt is written for the SDK publication-trailer writer.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/modulesign"
	"github.com/spacedatanetwork/sdn-server/internal/sigdomain"
	"github.com/spf13/cobra"
)

type moduleSignResponse struct {
	ContentHash     string                    `json:"content_hash"`
	StatementDomain string                    `json:"statement_domain"`
	Algorithm       string                    `json:"algorithm"`
	PublicKeyHex    string                    `json:"public_key_hex"`
	SignatureHex    string                    `json:"signature_hex"`
	PortableBytes   int                       `json:"portable_bytes"`
	TrailerStripped bool                      `json:"trailer_stripped"`
	SignedAt        string                    `json:"signed_at"`
	SignatureEntry  modulesign.SignatureEntry `json:"signature_entry"`
}

func init() {
	pluginsCmd.AddCommand(newModuleSignCommand(newAdminClient))
}

func newModuleSignCommand(connect func(*cobra.Command) (*adminClient, error)) *cobra.Command {
	var input, output, trustedKey string
	cmd := &cobra.Command{
		Use: "sign", Short: "Request and verify a content-bound module publication signature",
		Long: "Submit raw WASM to this node's authenticated signing endpoint. Verify the returned " +
			"signature against --trusted-public-key and write its public JSON receipt to a new --out file. " +
			"Use the module SDK to assemble and verify the publication trailer. No module is installed or published.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if input == "" || output == "" {
				return fmt.Errorf("--wasm and --out are required")
			}
			if _, err := modulePublicKey(trustedKey); err != nil {
				return fmt.Errorf("--trusted-public-key: %w", err)
			}
			file, err := os.Open(input)
			if err != nil {
				return fmt.Errorf("read module: %w", err)
			}
			defer file.Close()
			artifact, err := io.ReadAll(io.LimitReader(file, modulesign.MaxArtifactBytes+1))
			if err != nil {
				return fmt.Errorf("read module: %w", err)
			}
			if len(artifact) > modulesign.MaxArtifactBytes {
				return fmt.Errorf("module exceeds signing size limit")
			}
			if _, err := os.Stat(output); err == nil {
				return fmt.Errorf("output already exists: %s", output)
			} else if !os.IsNotExist(err) {
				return err
			}
			client, err := connect(cmd)
			if err != nil {
				return err
			}
			result, err := requestModuleSignature(cmd.Context(), client, artifact, trustedKey)
			if err != nil {
				return err
			}
			encoded, err := json.MarshalIndent(result, "", "  ")
			if err != nil {
				return err
			}
			if err := writeModuleSignatureReceipt(output, append(encoded, '\n')); err != nil {
				return fmt.Errorf("write signature receipt: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "signature_receipt=%s\ncontent_hash=%s\npublic_key_hex=%s\nverified_locally=yes\n", output, result.ContentHash, result.PublicKeyHex)
			return nil
		},
	}
	cmd.Flags().StringVar(&input, "wasm", "", "locally built WASM artifact")
	cmd.Flags().StringVar(&output, "out", "", "new public signature-receipt JSON file")
	cmd.Flags().StringVar(&trustedKey, "trusted-public-key", "", "expected publisher Ed25519 public key (64 hex characters)")
	addSessionTokenFlag(cmd)
	return cmd
}

// Link a fully written sibling into place exclusively. Failed writes never
// expose a partial receipt, and an existing destination is never overwritten.
func writeModuleSignatureReceipt(path string, payload []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".module-signature-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(payload); err != nil {
		file.Close()
		return err
	}
	if err := file.Chmod(0o644); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Link(file.Name(), path)
}

func requestModuleSignature(ctx context.Context, client *adminClient, artifact []byte, trustedKey string) (*moduleSignResponse, error) {
	const path = "/api/v1/admin/modules/sign"
	req, err := client.newRequest(ctx, http.MethodPost, path, bytes.NewReader(artifact))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/wasm")
	response, err := client.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request module signature: %w", err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(payload) > 1<<20 {
		return nil, fmt.Errorf("module signing response exceeds size limit")
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("module signing: %s: %s", response.Status, strings.TrimSpace(string(payload)))
	}
	var result moduleSignResponse
	if err := json.Unmarshal(payload, &result); err != nil {
		return nil, err
	}
	if err := verifyModuleSignResponse(artifact, trustedKey, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func modulePublicKey(value string) (ed25519.PublicKey, error) {
	key, err := hex.DecodeString(strings.TrimSpace(value))
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("expected a 32-byte Ed25519 public key in hex")
	}
	return ed25519.PublicKey(key), nil
}

func verifyModuleSignResponse(artifact []byte, trustedKey string, result *moduleSignResponse) error {
	pub, err := modulePublicKey(trustedKey)
	if err != nil {
		return err
	}
	portable := modulert.StripPublicationTrailer(artifact)
	sum := sha256.Sum256(portable)
	hash := hex.EncodeToString(sum[:])
	entry := result.SignatureEntry
	if result.ContentHash != hash || entry.SignedHashHex != hash || result.PortableBytes != len(portable) || result.TrailerStripped != (len(portable) != len(artifact)) {
		return fmt.Errorf("node signature does not describe the submitted portable module")
	}
	if result.StatementDomain != sigdomain.DomainModulePublicationV1 || entry.StatementDomain != sigdomain.DomainModulePublicationV1 ||
		result.Algorithm != modulesign.SignatureAlgorithm || entry.Algorithm != modulesign.SignatureAlgorithm || entry.SignedHashAlgorithm != modulesign.SignedHashAlgorithm {
		return fmt.Errorf("node signature has an unexpected domain or algorithm")
	}
	if result.PublicKeyHex != hex.EncodeToString(pub) || entry.PublicKeyHex != result.PublicKeyHex || entry.SignatureHex != result.SignatureHex {
		return fmt.Errorf("node signature does not match the trusted publisher or its SDK entry")
	}
	statement, err := sigdomain.Statement(sigdomain.DomainModulePublicationV1, sum[:])
	if err != nil {
		return err
	}
	signature, err := hex.DecodeString(result.SignatureHex)
	if err != nil || !ed25519.Verify(pub, statement, signature) {
		return fmt.Errorf("node returned an invalid module signature")
	}
	return nil
}

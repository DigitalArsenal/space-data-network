package main

// `spacedatanetwork dns-proof` mints the signed domain-to-nodekey proof an
// operator pastes into DNS as a TXT record.
//
// Design constraints that shaped this command, all of them deliberate:
//
//   - IT NEVER WRITES. No keystore mutation, no config mutation, no daemon
//     contact. That is what makes it safe to run as a one-shot inspection on a
//     production box while the daemon keeps serving, rather than requiring a
//     guarded cutover (Seal Council, Hephaestus 2026-07-30: read-only, no
//     restart, no binary replacement = an inspection).
//   - THE PRIVATE KEY NEVER LEAVES. The seed is decrypted in this process,
//     signs one statement, and the process exits. Only the public key and the
//     signature reach stdout.
//   - IT REGISTERS ITSELF from init() instead of editing main.go, because
//     main.go is a hot shared file and this command owes it nothing.
//   - IT VERIFIES ITS OWN OUTPUT before printing (dnsproof.Sign does this),
//     because a record that cannot verify sends an operator to edit DNS for
//     nothing.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/dnsproof"
	"github.com/spacedatanetwork/sdn-server/internal/keys"
	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

var (
	dnsProofDomain   string
	dnsProofSelector string
	dnsProofValidFor time.Duration
	dnsProofJSON     bool
	dnsProofAccount  uint32
)

var dnsProofCmd = &cobra.Command{
	Use:   "dns-proof",
	Short: "Generate the signed DNS TXT proof binding a domain to this node's key",
	Long: `Generate the signed proof that binds a DNS domain to this node's signing key.

The proof is published as a DNS TXT record at _sdnkey.<domain>. It binds the
domain and the key in BOTH directions: publishing the record proves control of
DNS, and the signature over a canonical statement that NAMES the domain proves
control of the key. A verifier rebuilds that statement from the domain it
queried, so a record copied into another zone cannot verify.

This command reads the node's key material and writes NOTHING. The private key
never leaves the process; only the public key and the signature are printed.`,
	RunE: runDNSProof,
}

func init() {
	dnsProofCmd.Flags().StringVar(&dnsProofDomain, "domain", "", "domain to bind (required), e.g. sdn.spaceaware.io")
	dnsProofCmd.Flags().StringVar(&dnsProofSelector, "selector", "",
		"optional single-label selector for multi-key domains: <selector>._sdnkey.<domain>")
	dnsProofCmd.Flags().DurationVar(&dnsProofValidFor, "valid-for", 365*24*time.Hour,
		"validity period; 0 means no expiry (NOT recommended: expiry bounds the blast radius of a lost key)")
	dnsProofCmd.Flags().BoolVar(&dnsProofJSON, "json", false, "emit JSON for programmatic use (dashboard menu)")
	dnsProofCmd.Flags().Uint32Var(&dnsProofAccount, "account", 0, "BIP-44 account index")
	rootCmd.AddCommand(dnsProofCmd)
}

// dnsProofOutput is the machine-readable form. Field names are lowercase
// because these are API-synthesized fields, not SDS record keys (SDS keys match
// IDL capitalization exactly; this is not an SDS record — see the ruling in
// graph/tasks/sdn-dns-key-proof-standard.md).
type dnsProofOutput struct {
	OwnerName     string `json:"owner_name"`
	RecordType    string `json:"record_type"`
	RecordValue   string `json:"record_value"`
	Domain        string `json:"domain"`
	Algorithm     string `json:"algorithm"`
	PublicKeyHex  string `json:"public_key_hex"`
	PeerID        string `json:"peer_id"`
	KeyPath       string `json:"key_path"`
	IssuedAt      int64  `json:"issued_at"`
	ExpiresAt     int64  `json:"expires_at"`
	Statement     string `json:"canonical_statement"`
	RecordBytes   int    `json:"record_bytes"`
	MultiString   bool   `json:"requires_multiple_dns_strings"`
	Fingerprint   string `json:"key_fingerprint"`
	VerifiedLocal bool   `json:"verified_locally"`
}

// dnsProofRequest is everything the proof needs that is NOT secret, plus one
// signing closure. Splitting it out this way is what makes the command testable
// without a keystore: a test supplies its own key and closure and gets the exact
// bytes an operator would paste.
type dnsProofRequest struct {
	Domain    string
	Selector  string
	PublicKey []byte
	PeerID    string
	KeyPath   string
	IssuedAt  time.Time
	ValidFor  time.Duration
	Sign      func(statement []byte) ([]byte, error)
}

func buildDNSProofOutput(req dnsProofRequest) (dnsProofOutput, error) {
	ownerName, err := dnsproof.OwnerName(req.Domain, req.Selector)
	if err != nil {
		return dnsProofOutput{}, err
	}
	var expiresAt int64
	if req.ValidFor > 0 {
		expiresAt = req.IssuedAt.Add(req.ValidFor).Unix()
	}
	signed, err := dnsproof.Sign(dnsproof.Proof{
		Domain:    req.Domain,
		Algorithm: dnsproof.AlgEd25519,
		PublicKey: req.PublicKey,
		PeerID:    req.PeerID,
		IssuedAt:  req.IssuedAt.Unix(),
		ExpiresAt: expiresAt,
	}, req.Sign)
	if err != nil {
		return dnsProofOutput{}, err
	}
	record, err := signed.Record()
	if err != nil {
		return dnsProofOutput{}, err
	}
	statement, err := dnsproof.CanonicalStatement(signed)
	if err != nil {
		return dnsProofOutput{}, err
	}
	return dnsProofOutput{
		OwnerName:     ownerName,
		RecordType:    "TXT",
		RecordValue:   record,
		Domain:        signed.Domain,
		Algorithm:     signed.Algorithm,
		PublicKeyHex:  hex.EncodeToString(signed.PublicKey),
		PeerID:        signed.PeerID,
		KeyPath:       req.KeyPath,
		IssuedAt:      signed.IssuedAt,
		ExpiresAt:     signed.ExpiresAt,
		Statement:     string(statement),
		RecordBytes:   len(record),
		MultiString:   dnsproof.RecordExceedsSingleString(record),
		Fingerprint:   signed.KeyFingerprint(),
		VerifiedLocal: dnsproof.Verify(signed, req.IssuedAt) == nil,
	}, nil
}

func runDNSProof(cmd *cobra.Command, args []string) error {
	if dnsProofDomain == "" {
		return fmt.Errorf("--domain is required")
	}
	// Validate the domain BEFORE touching the keystore: there is no reason to
	// decrypt a seed to find out the operator typed a bare hostname.
	if _, err := dnsproof.OwnerName(dnsProofDomain, dnsProofSelector); err != nil {
		return err
	}

	cfg, cfgRes, err := config.LoadResolved(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	fmt.Fprintf(os.Stderr, "config: %s\n", cfgRes.Describe())

	keyPassword := os.Getenv("SDN_KEY_PASSWORD")
	if keyPassword == "" {
		keyPassword = cfg.Security.KeyPassword
	}
	if keyPassword == "" {
		keyPassword = keys.DeriveDefaultPassword()
	}

	keyDir := config.KeyDir(cfg)
	mnemonicPath := config.MnemonicPathResolved(cfg)
	keys.WarnKeyDirPermissions(keyDir)
	if err := keys.EnforceKeyFilePermissions(mnemonicPath); err != nil {
		return err
	}
	data, err := os.ReadFile(mnemonicPath)
	if err != nil {
		if os.IsNotExist(err) {
			return config.DescribeMissingNodeState("node identity (mnemonic)", mnemonicPath, cfgRes)
		}
		if os.IsPermission(err) {
			return config.DescribePermissionDenied("the node mnemonic", mnemonicPath, keyDirOwner(keyDir), cfgRes)
		}
		return fmt.Errorf("read mnemonic %s (config: %s): %w", mnemonicPath, cfgRes.Describe(), err)
	}

	mnemonic := string(data)
	if keys.IsMnemonicEncrypted(data) {
		mnemonic, err = keys.DecryptMnemonic(data, keyPassword)
		if err != nil {
			return fmt.Errorf("failed to decrypt mnemonic (wrong password?): %w", err)
		}
	}

	wp, err := resolveHDWalletWasmPath()
	if err != nil {
		return err
	}
	ctx := context.Background()
	hw, err := wasm.NewHDWalletModule(ctx, wp)
	if err != nil {
		return fmt.Errorf("failed to load HD wallet WASM: %w", err)
	}
	defer hw.Close(ctx)

	seed, err := hw.MnemonicToSeed(ctx, mnemonic, "")
	if err != nil {
		return fmt.Errorf("failed to derive seed: %w", err)
	}
	identity, err := hw.DeriveIdentity(ctx, seed, dnsProofAccount)
	if err != nil {
		return fmt.Errorf("failed to derive identity: %w", err)
	}

	// The Ed25519 EPM/publication signing key is the one that signs domain
	// proofs (Themis ruling + Hephaestus concurrence, 2026-07-30): it is the key
	// the node already publishes as its signing key and the key that signs
	// modules, so manifest signature -> node key -> domain proof is ONE chain
	// with no curve bridge. The bond reaches it through the EPM CHAIN_PROOFS,
	// which sign a payload naming this same public key.
	signingPub, err := identity.SigningPubKey.Raw()
	if err != nil {
		return fmt.Errorf("failed to read the signing public key: %w", err)
	}

	out, err := buildDNSProofOutput(dnsProofRequest{
		Domain:    dnsProofDomain,
		Selector:  dnsProofSelector,
		PublicKey: signingPub,
		PeerID:    identity.PeerID.String(),
		KeyPath:   identity.SigningKeyPath,
		IssuedAt:  time.Now().UTC(),
		ValidFor:  dnsProofValidFor,
		// libp2p's Ed25519 Sign is raw RFC 8032 over the message, which is
		// exactly what the browser verifier checks.
		Sign: identity.SigningPrivKey.Sign,
	})
	if err != nil {
		return err
	}

	if dnsProofJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	// Human form: the two things an operator has to paste, then the context they
	// need to check it. stdout carries ONLY the record value on its own line so
	// `dns-proof --domain x | tail -1` is a usable pipeline; everything else is
	// stderr.
	fmt.Fprintf(os.Stderr, "\n--- SDN domain proof ---\n")
	fmt.Fprintf(os.Stderr, "Publish this DNS record:\n\n")
	fmt.Fprintf(os.Stderr, "  name:  %s\n", out.OwnerName)
	fmt.Fprintf(os.Stderr, "  type:  TXT\n")
	fmt.Fprintf(os.Stderr, "  value: %s\n\n", out.RecordValue)
	fmt.Fprintf(os.Stderr, "Signed by:   %s (%s)\n", out.Algorithm, out.KeyPath)
	fmt.Fprintf(os.Stderr, "Public key:  %s\n", out.PublicKeyHex)
	fmt.Fprintf(os.Stderr, "Peer ID:     %s\n", out.PeerID)
	fmt.Fprintf(os.Stderr, "Issued:      %s\n", time.Unix(out.IssuedAt, 0).UTC().Format(time.RFC3339))
	if out.ExpiresAt == 0 {
		fmt.Fprintf(os.Stderr, "Expires:     never (re-issue with --valid-for to bound key loss)\n")
	} else {
		fmt.Fprintf(os.Stderr, "Expires:     %s\n", time.Unix(out.ExpiresAt, 0).UTC().Format(time.RFC3339))
	}
	fmt.Fprintf(os.Stderr, "Record size: %d bytes\n", out.RecordBytes)
	if out.MultiString {
		fmt.Fprintf(os.Stderr,
			"WARNING:     over 255 bytes, so DNS will split it into multiple character-strings.\n"+
				"             That is legal and verifiers handle it, but some registrar consoles\n"+
				"             mangle long values — check the record reads back byte-for-byte.\n")
	}
	fmt.Fprintf(os.Stderr, "\nCanonical statement covered by the signature:\n%s\n", out.Statement)
	fmt.Fprintf(os.Stderr,
		"Nothing was written. This node's keystore and config are unchanged.\n")

	fmt.Println(out.RecordValue)
	return nil
}

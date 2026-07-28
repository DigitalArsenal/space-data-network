// `spacedatanetwork key escrow create|recover`
//
// Escrow is the only protection that survives a destroy+recreate: at-rest
// encryption goes down with the disk. See internal/escrow for the design and
// for why derivation is preferred over escrow wherever an identity is
// re-creatable from the root seed.

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	secp256k1 "github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/spf13/cobra"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/ecies"
	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/escrow"
	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

var (
	escrowCreateRepo    string
	escrowCreateRole    string
	escrowCreateOutput  string
	escrowCreateAccount uint32
	escrowRecoverInput  string
	escrowRecoverRepo   string
	escrowRecoverForce  bool
)

var keyEscrowCmd = &cobra.Command{
	Use:   "escrow",
	Short: "Seal a recoverable copy of a node identity, and recover from one",
	Long: `Seal a recoverable copy of a node identity, and recover from one.

At-rest encryption does not survive a rebuild — a wiped disk takes the
ciphertext with it. Escrow is the copy that lives somewhere else, sealed to this
node's advertised encryption key so ONLY the root wallet (the mnemonic) can open
it. The sealed blob is safe to replicate or hand to an operator.

Identities that can be RE-DERIVED from the root seed need no sealed payload: the
escrow record simply states the derivation path, and the mnemonic is the backup.
That is the default for this node's own identity. Escrow of actual key material
is for identities that cannot be re-derived — chiefly a kubo peer key, which
'ipfs init' generates randomly and whose PeerID must never change.`,
}

var keyEscrowCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create an escrow record for this node, or for a kubo repo identity",
	Long: `Create an escrow record.

Without --repo, records THIS node's identity. That identity is HD-derived, so
the record states its derivation path and seals nothing — the mnemonic recovers
it.

With --repo, seals the kubo repo's peer private key, which cannot be re-derived.
The sealed blob is safe to store off-box; only the root wallet opens it.`,
	RunE: runKeyEscrowCreate,
}

var keyEscrowRecoverCmd = &cobra.Command{
	Use:   "recover",
	Short: "Recover an identity from an escrow record",
	Long: `Recover an identity from an escrow record.

Requires this node's mnemonic, from which the recovery key at the record's
derivation path is derived. Writing a recovered kubo identity requires --repo;
key material is never printed to a terminal.`,
	RunE: runKeyEscrowRecover,
}

func init() {
	keyEscrowCreateCmd.Flags().StringVar(&escrowCreateRepo, "repo", "",
		"kubo repo path whose peer identity to seal (default: record THIS node's derivable identity)")
	keyEscrowCreateCmd.Flags().StringVar(&escrowCreateRole, "role", "",
		"operator label recorded in the blob, e.g. kubo-od-producer")
	keyEscrowCreateCmd.Flags().StringVarP(&escrowCreateOutput, "output", "o", "",
		"write the escrow record to a file instead of stdout")
	keyEscrowCreateCmd.Flags().Uint32Var(&escrowCreateAccount, "account", 0,
		"HD account index for the recovery key path")

	keyEscrowRecoverCmd.Flags().StringVarP(&escrowRecoverInput, "input", "i", "-",
		"escrow record to read, or '-' for stdin")
	keyEscrowRecoverCmd.Flags().StringVar(&escrowRecoverRepo, "repo", "",
		"kubo repo path to restore the recovered identity into")
	keyEscrowRecoverCmd.Flags().BoolVar(&escrowRecoverForce, "force", false,
		"overwrite a DIFFERENT identity already present in --repo (destructive)")

	keyEscrowCmd.AddCommand(keyEscrowCreateCmd)
	keyEscrowCmd.AddCommand(keyEscrowRecoverCmd)
	keyCmd.AddCommand(keyEscrowCmd)
}

// escrowRecoveryKey derives the node's advertised encryption keypair — the
// xpub-derivable secp256k1 key at m/44'/0'/account'/1/0 — from the stored
// mnemonic. Its public half seals an escrow; its private half opens one.
//
// Deriving from the mnemonic (not from a stored key) is what makes the owner's
// wallet, and only the owner's wallet, the recovery authority.
func escrowRecoveryKey(ctx context.Context, cfg *config.Config, res config.Resolution, account uint32) (priv, pub []byte, path string, xpub string, err error) {
	mnemonic, _, err := loadStoredMnemonic(cfg, res)
	if err != nil {
		return nil, nil, "", "", err
	}
	wp, err := resolveHDWalletWasmPath()
	if err != nil {
		return nil, nil, "", "", err
	}
	hw, err := wasm.NewHDWalletModule(ctx, wp)
	if err != nil {
		return nil, nil, "", "", fmt.Errorf("load HD wallet WASM: %w", err)
	}
	defer hw.Close(ctx)

	seed, err := hw.MnemonicToSeed(ctx, mnemonic, "")
	if err != nil {
		return nil, nil, "", "", fmt.Errorf("derive seed: %w", err)
	}
	xpub, err = hw.DeriveXPub(ctx, seed, account)
	if err != nil {
		return nil, nil, "", "", fmt.Errorf("derive xpub: %w", err)
	}
	_, path = epm.EffectiveKeyPaths(nil, account)

	derived, err := hw.DeriveSecp256k1Key(ctx, seed, path)
	if err != nil {
		return nil, nil, "", "", fmt.Errorf("derive recovery key at %s: %w", path, err)
	}
	// The advertised encryption key is secp256k1 (xpub-derivable), matching
	// ecies.Secp256k1 — the same curve a verifier reconstructs from the xpub.
	sk := secp256k1.PrivKeyFromBytes(derived.PrivateKey)
	return sk.Serialize(), sk.PubKey().SerializeCompressed(), path, xpub, nil
}

// readEscrowInput reads an escrow record from a file or stdin.
func readEscrowInput(cmd *cobra.Command, source string) ([]byte, error) {
	if s := strings.TrimSpace(source); s != "" && s != "-" {
		data, err := os.ReadFile(s)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", s, err)
		}
		return data, nil
	}
	data, err := io.ReadAll(cmd.InOrStdin())
	if err != nil {
		return nil, fmt.Errorf("read escrow record from stdin: %w", err)
	}
	return data, nil
}

func runKeyEscrowCreate(cmd *cobra.Command, _ []string) error {
	cfg, res, err := config.LoadResolved(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	_, recipientPub, keyPath, xpub, err := escrowRecoveryKey(ctx, cfg, res, escrowCreateAccount)
	if err != nil {
		return err
	}
	recipient := escrow.Recipient{XPub: xpub, KeyPath: keyPath, PublicKey: fmt.Sprintf("%x", recipientPub)}

	var blob *escrow.Blob
	if repo := strings.TrimSpace(escrowCreateRepo); repo != "" {
		role := escrowCreateRole
		if role == "" {
			role = "kubo-peer"
		}
		blob, err = escrow.SealKuboRepo(repo, recipient, recipientPub, ecies.Secp256k1, role)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.ErrOrStderr(),
			"Sealed kubo peer identity %s to %s.\nOnly the root mnemonic can open this record.\n",
			blob.Subject.PeerID, keyPath)
	} else {
		blob, err = escrowThisNodeIdentity(ctx, cfg, res, recipient)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.ErrOrStderr(),
			"Recorded this node's DERIVABLE identity %s at path %s.\n"+
				"No key material is sealed: the mnemonic re-creates this identity.\n",
			blob.Subject.PeerID, blob.Subject.DerivationPath)
	}

	data, err := blob.Marshal()
	if err != nil {
		return err
	}
	if out := strings.TrimSpace(escrowCreateOutput); out != "" {
		if err := blob.WriteFile(out); err != nil {
			return fmt.Errorf("write escrow record: %w", err)
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "Wrote %s (mode 0600). Keep a copy OFF this machine.\n", out)
		return nil
	}
	_, err = cmd.OutOrStdout().Write(data)
	return err
}

// escrowThisNodeIdentity records the node's own HD identity as a derivation
// pointer rather than sealed material.
func escrowThisNodeIdentity(ctx context.Context, cfg *config.Config, res config.Resolution, recipient escrow.Recipient) (*escrow.Blob, error) {
	mnemonic, _, err := loadStoredMnemonic(cfg, res)
	if err != nil {
		return nil, err
	}
	wp, err := resolveHDWalletWasmPath()
	if err != nil {
		return nil, err
	}
	hw, err := wasm.NewHDWalletModule(ctx, wp)
	if err != nil {
		return nil, fmt.Errorf("load HD wallet WASM: %w", err)
	}
	defer hw.Close(ctx)

	identity, err := hw.IdentityFromMnemonic(ctx, mnemonic, "", escrowCreateAccount)
	if err != nil {
		return nil, fmt.Errorf("derive node identity: %w", err)
	}
	info := identity.Info()
	hostname, _ := os.Hostname()
	role := escrowCreateRole
	if role == "" {
		role = "sdn-node"
	}
	blob, err := escrow.NewDerivable(escrow.Subject{
		PeerID:         info.PeerID,
		KeyType:        escrow.KeyTypeLibp2pSecp256k1,
		DerivationPath: info.IdentityKeyPath,
		MachineName:    hostname,
		Role:           role,
	})
	if err != nil {
		return nil, err
	}
	blob.Recipient = recipient
	return blob, nil
}

func runKeyEscrowRecover(cmd *cobra.Command, _ []string) error {
	raw, err := readEscrowInput(cmd, escrowRecoverInput)
	if err != nil {
		return err
	}
	blob, err := escrow.Parse(raw)
	if err != nil {
		return err
	}

	// A derivation-only record needs no recovery key at all.
	if blob.Derivable() {
		fmt.Fprintf(cmd.OutOrStdout(),
			"This record is derivation-only — nothing is sealed.\n"+
				"  PeerID:          %s\n"+
				"  DerivationPath:  %s\n"+
				"  Machine:         %s\n\n"+
				"Recover it by restoring the root mnemonic on the target node:\n"+
				"  spacedatanetwork key import --format mnemonic\n",
			blob.Subject.PeerID, blob.Subject.DerivationPath, blob.Subject.MachineName)
		return nil
	}

	cfg, res, err := config.LoadResolved(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	recipientPriv, _, _, _, err := escrowRecoveryKey(ctx, cfg, res, escrowCreateAccount)
	if err != nil {
		return err
	}

	repo := strings.TrimSpace(escrowRecoverRepo)
	if repo == "" {
		// Never print private key material to a terminal. Report what the
		// record would restore so an operator can confirm before acting.
		if _, err := blob.Open(recipientPriv); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(),
			"Escrow opens successfully with this node's recovery key.\n"+
				"  PeerID:   %s\n"+
				"  KeyType:  %s\n"+
				"  Machine:  %s\n"+
				"  Role:     %s\n\n"+
				"Pass --repo <kubo repo> to restore it. Key material is never printed.\n",
			blob.Subject.PeerID, blob.Subject.KeyType, blob.Subject.MachineName, blob.Subject.Role)
		return nil
	}

	recovered, err := escrow.RecoverKuboRepo(blob, recipientPriv, repo, escrowRecoverForce)
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.ErrOrStderr(),
		"Restored peer identity %s into %s.\n"+
			"Identity.PrivKey is PLAINTEXT base64 at mode 0600 — the only form kubo can read.\n",
		recovered, repo)
	return nil
}

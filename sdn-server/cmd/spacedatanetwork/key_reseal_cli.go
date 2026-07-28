package main

// key_reseal_cli.go — `spacedatanetwork key reseal`.
//
// WHY THIS EXISTS, precisely.
//
// A node's material is sealed at rest under a password that, by default, is
// DERIVED FROM THE MACHINE. That is good custody and it is deliberately not
// portable: the whole point is that a stolen disk opens nowhere else.
//
// It also means a node cannot MOVE. On 2026-07-28 the CelesTrak retriever had
// to move from host-01 to the celestrak.eth box, carrying its peer identity —
// its unit fed a password FILE precisely so the identity would be portable, and
// the daemon was ignoring that file and sealing under the machine instead. Even
// with that fixed, everything already on disk was sealed to the old machine.
//
// The daemon does NOT resolve this silently. A pinned password must not be
// bypassed by a machine-derived one just because the machine-derived one
// happens to work (internal/node: TestMigrationSkippedWhenPassphraseConfigured
// — a rule owned by the at-rest key work, deliberately kept). So the re-seal is
// an OPERATOR ACTION, typed on the box that can still open the material, with
// the destination password already in place. Explicit, logged, and refusing to
// guess.
//
// It changes no key material: same mnemonic, same node key, same PeerID. Only
// the envelope changes.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/keys"
	"github.com/spf13/cobra"
)

var keyResealConfirm bool

// nodeKeyEncPrefix mirrors internal/node's nodeKeyEncMagic — the marker that
// says a node.key file is sealed rather than raw. Duplicated here (8 bytes,
// wire-stable, and a load-bearing test below pins them together) rather than
// exporting the daemon's internal for one CLI read.
const nodeKeyEncPrefix = "sdnkey1\x00"

var keyResealCmd = &cobra.Command{
	Use:   "reseal",
	Short: "Re-seal this node's at-rest key material under the currently configured key password",
	Long: `Re-encrypt this node's mnemonic and node key under the password this node
resolves NOW (SDN_KEY_PASSWORD, SDN_KEY_PASSWORD_FILE, or config
security.key_password), recovering them with the machine-derived password they
are currently sealed under.

This is the supported way to make an identity PORTABLE before moving a node to
another machine: run it on the box that can still open the material, with the
destination password already configured here, then copy the key directory.

No key material changes: same mnemonic, same node key, same PeerID. Only the
envelope changes. The daemon will not do this silently — a pinned password must
never be bypassed by a machine-derived one without an operator asking.`,
	RunE: runKeyReseal,
}

func init() {
	keyResealCmd.Flags().BoolVar(&keyResealConfirm, "confirm", false,
		"required: acknowledge that this rewrites the at-rest envelope of this node's key material")
	keyCmd.AddCommand(keyResealCmd)
}

func runKeyReseal(cmd *cobra.Command, args []string) error {
	cfg, _, err := config.LoadResolved(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	target, err := config.KeyPassword(cfg)
	if err != nil {
		return fmt.Errorf("resolve destination key password: %w", err)
	}
	if strings.TrimSpace(target) == "" {
		return fmt.Errorf(
			"no explicit key password is configured here, so there is nothing to re-seal TO.\n" +
				"Set SDN_KEY_PASSWORD_FILE (preferred: a 0600 file, invisible to ps and docker inspect), " +
				"SDN_KEY_PASSWORD, or security.key_password, then re-run.")
	}

	keyDir := config.KeyDir(cfg)
	mnemonicPath := config.MnemonicPathResolved(cfg)
	if strings.TrimSpace(mnemonicPath) == "" {
		mnemonicPath = filepath.Join(keyDir, "mnemonic")
	}
	nodeKeyPath := filepath.Join(keyDir, "node.key")

	if !keyResealConfirm {
		return fmt.Errorf(
			"refusing to re-seal without --confirm.\n"+
				"  key directory: %s\n"+
				"  mnemonic:      %s\n"+
				"  node key:      %s\n"+
				"This rewrites the at-rest envelope of the files above under the currently "+
				"configured password. Key material and PeerID are unchanged.", keyDir, mnemonicPath, nodeKeyPath)
	}

	out := cmd.OutOrStdout()
	resealed := 0

	// MNEMONIC — the root of the identity.
	if data, rerr := os.ReadFile(mnemonicPath); rerr == nil && keys.IsMnemonicEncrypted(data) {
		if _, derr := keys.DecryptMnemonic(data, target); derr == nil {
			fmt.Fprintf(out, "  mnemonic: already sealed under the configured password (%s)\n", mnemonicPath)
		} else {
			recovered, scheme, aerr := keys.DecryptMnemonicAnyScheme(data)
			if aerr != nil {
				return fmt.Errorf("cannot open the mnemonic at %s with the configured password OR any machine-derived generation: %w\n%s",
					mnemonicPath, aerr, keys.DerivationFailureHint(keyDir))
			}
			sealed, eerr := keys.EncryptMnemonic(strings.TrimSpace(recovered), target)
			if eerr != nil {
				return fmt.Errorf("re-encrypt mnemonic: %w", eerr)
			}
			if werr := os.WriteFile(mnemonicPath, sealed, 0o600); werr != nil {
				return fmt.Errorf("write re-sealed mnemonic: %w", werr)
			}
			fmt.Fprintf(out, "  mnemonic: re-sealed from the %s-derived password (%s)\n", scheme, mnemonicPath)
			resealed++
		}
	} else if rerr == nil {
		fmt.Fprintf(out, "  mnemonic: stored in PLAINTEXT at %s — nothing to re-seal (the daemon seals it on next start)\n", mnemonicPath)
	}

	// NODE KEY — the libp2p identity file, sealed with its own magic prefix.
	if raw, rerr := os.ReadFile(nodeKeyPath); rerr == nil {
		body, isSealed := strings.CutPrefix(string(raw), nodeKeyEncPrefix)
		switch {
		case !isSealed:
			fmt.Fprintf(out, "  node key: not encrypted at rest (%s) — the daemon seals it on next start\n", nodeKeyPath)
		default:
			if _, derr := keys.DecryptSecret([]byte(body), target); derr == nil {
				fmt.Fprintf(out, "  node key: already sealed under the configured password (%s)\n", nodeKeyPath)
			} else {
				recovered, scheme, aerr := keys.DecryptSecretAnyScheme([]byte(body))
				if aerr != nil {
					return fmt.Errorf("cannot open the node key at %s with the configured password OR any machine-derived generation: %w\n%s",
						nodeKeyPath, aerr, keys.DerivationFailureHint(keyDir))
				}
				sealed, eerr := keys.EncryptSecret(recovered, target)
				if eerr != nil {
					return fmt.Errorf("re-encrypt node key: %w", eerr)
				}
				if werr := os.WriteFile(nodeKeyPath, append([]byte(nodeKeyEncPrefix), sealed...), 0o600); werr != nil {
					return fmt.Errorf("write re-sealed node key: %w", werr)
				}
				fmt.Fprintf(out, "  node key: re-sealed from the %s-derived password (%s)\n", scheme, nodeKeyPath)
				resealed++
			}
		}
	}

	if resealed == 0 {
		fmt.Fprintln(out, "Nothing needed re-sealing: this node's material already opens with the configured password.")
		return nil
	}
	fmt.Fprintf(out, "Re-sealed %d file(s). The identity is unchanged and is now portable with its password file.\n", resealed)
	return nil
}

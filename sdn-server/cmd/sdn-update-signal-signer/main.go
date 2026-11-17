// Command sdn-update-signal-signer signs and verifies the update signal of
// every entry of a release's update feed. The release workflow builds it from
// the checked-out commit inside the job that holds the update signing key, so
// no program built by another job runs with the key. It needs no cgo and no
// WasmEdge.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/spacedatanetwork/sdn-server/internal/update"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "sdn-update-signal-signer:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("sdn-update-signal-signer", flag.ContinueOnError)
	keyEnv := flags.String("key-env", "", "environment variable holding the PKCS8 Ed25519 signing key PEM")
	dir := flags.String("feed-dir", "", "update feed directory")
	rootsPath := flags.String("roots", "", "trusted update roots file")
	verifyOnly := flags.Bool("verify-only", false, "verify the signals without signing")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if *dir == "" || *rootsPath == "" {
		return errors.New("--feed-dir and --roots are required")
	}
	if *verifyOnly == (*keyEnv != "") {
		return errors.New("provide exactly one of --key-env or --verify-only")
	}
	roots, err := update.ReadTrustRootsFile(*rootsPath)
	if err != nil {
		return err
	}
	if !*verifyOnly {
		key, err := update.ParseSigningKeyPEM([]byte(os.Getenv(*keyEnv)))
		if err != nil {
			return err
		}
		if err := update.SignFeedSignals(*dir, key, roots); err != nil {
			return err
		}
	}
	paths, err := update.VerifyFeedSignalDir(*dir, roots)
	if err != nil {
		return err
	}
	for _, path := range paths {
		fmt.Printf("verified=%s\n", path)
	}
	return nil
}

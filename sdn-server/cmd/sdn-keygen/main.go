package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

type result struct {
	PeerID  string `json:"peerId"`
	KeyPath string `json:"keyPath"`
	Created bool   `json:"created"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	outPath := flag.String("out", "", "path to write or read the libp2p private key")
	jsonOutput := flag.Bool("json", false, "print JSON output")
	force := flag.Bool("force", false, "replace an existing key")
	flag.Parse()

	keyPath := strings.TrimSpace(*outPath)
	if keyPath == "" {
		return errors.New("--out is required")
	}
	absKeyPath, err := filepath.Abs(keyPath)
	if err != nil {
		return fmt.Errorf("resolve key path: %w", err)
	}

	privKey, created, err := loadOrCreateKey(absKeyPath, *force)
	if err != nil {
		return err
	}
	peerID, err := peer.IDFromPrivateKey(privKey)
	if err != nil {
		return fmt.Errorf("derive peer id: %w", err)
	}

	output := result{
		PeerID:  peerID.String(),
		KeyPath: absKeyPath,
		Created: created,
	}
	if *jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		return encoder.Encode(output)
	}

	fmt.Println(output.PeerID)
	return nil
}

func loadOrCreateKey(keyPath string, force bool) (libp2pcrypto.PrivKey, bool, error) {
	if !force {
		if raw, err := os.ReadFile(keyPath); err == nil {
			privKey, err := libp2pcrypto.UnmarshalPrivateKey(raw)
			if err != nil {
				return nil, false, fmt.Errorf("unmarshal existing key %s: %w", keyPath, err)
			}
			if err := os.Chmod(keyPath, 0644); err != nil {
				return nil, false, fmt.Errorf("make key readable by local containers: %w", err)
			}
			return privKey, false, nil
		} else if !os.IsNotExist(err) {
			return nil, false, fmt.Errorf("read existing key %s: %w", keyPath, err)
		}
	}

	privKey, _, err := libp2pcrypto.GenerateSecp256k1Key(rand.Reader)
	if err != nil {
		return nil, false, fmt.Errorf("generate secp256k1 key: %w", err)
	}
	raw, err := libp2pcrypto.MarshalPrivateKey(privKey)
	if err != nil {
		return nil, false, fmt.Errorf("marshal private key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0755); err != nil {
		return nil, false, fmt.Errorf("create key directory: %w", err)
	}
	if err := os.WriteFile(keyPath, raw, 0644); err != nil {
		return nil, false, fmt.Errorf("write key %s: %w", keyPath, err)
	}
	return privKey, true, nil
}

// Package admincli is the server-side wizard that enrolls a node admin, so a
// remote node (one with no desktop app in front of it) can be administered
// from the browser dashboard.
//
// The wizard prints the server's public keys first — the admin compares the
// encryption-key fingerprint when the dashboard first connects — then takes
// the admin's identity from one of three sources:
//
//   - username + password, derived exactly as the dashboard's wallet does;
//   - a BIP-39 recovery phrase, derived exactly as the dashboard's wallet does;
//   - a pasted Ed25519 sign-in public key, for a wallet made elsewhere.
//
// Only the public sign-in key and a row key are stored. Passwords and phrases
// are read without echo, used once and zeroed; they never reach disk or logs.
package admincli

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/walletderive"
)

// MinPasswordChars is the shortest password the wizard enrolls. The legacy
// password profile has no key stretching and an admin's sign-in public key is
// published, so a short password could be recovered offline.
const MinPasswordChars = 16

// Store is where the admin row lands: the running daemon's user API, or the
// auth database directly when the daemon is down.
type Store interface {
	HasAdmin(ctx context.Context) (bool, error)
	AddUser(ctx context.Context, rowKey, name string, trust peers.TrustLevel, signingPubKeyHex string) error
	Describe() string
}

// ServerKeys are the node's public keys as the wizard prints them.
type ServerKeys struct {
	PeerID            string
	SigningPubKeyHex  string
	SigningKeyPath    string
	EncryptionPubHex  string
	EncryptionKeyPath string
}

// Source selects where the admin's identity comes from.
type Source string

const (
	SourceAsk       Source = ""
	SourcePassword  Source = "password"
	SourceMnemonic  Source = "mnemonic"
	SourcePublicKey Source = "pubkey"
)

// ParseSource accepts the --source flag's spellings.
func ParseSource(s string) (Source, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return SourceAsk, nil
	case "password", "username-password", "userpass":
		return SourcePassword, nil
	case "mnemonic", "phrase", "recovery-phrase", "bip39":
		return SourceMnemonic, nil
	case "pubkey", "public-key", "key":
		return SourcePublicKey, nil
	}
	return "", fmt.Errorf("unknown source %q (want password, mnemonic or pubkey)", s)
}

// Options are the wizard's non-interactive answers. Anything left empty is
// asked for, unless the terminal is not interactive.
type Options struct {
	Name      string
	Source    Source
	Username  string
	PublicKey string
	// SecretFromStdin reads the password or phrase as one line of standard
	// input instead of prompting, for automation.
	SecretFromStdin bool
	// Yes skips the final confirmation.
	Yes bool
}

// Terminal is the wizard's view of the operator's console.
type Terminal struct {
	In  *bufio.Reader
	Out io.Writer
	// ReadSecret reads one line without echo. Nil means no interactive
	// terminal is attached.
	ReadSecret func(prompt string) ([]byte, error)
}

func (t Terminal) interactive() bool { return t.ReadSecret != nil }

// PrintServerKeys writes the node's public keys and the fingerprint the admin
// checks when the dashboard first connects.
func PrintServerKeys(w io.Writer, k ServerKeys) {
	fmt.Fprintln(w, "This server")
	fmt.Fprintf(w, "  Peer ID          %s\n", k.PeerID)
	fmt.Fprintf(w, "  Signing key      %s  (%s)\n", k.SigningPubKeyHex, k.SigningKeyPath)
	if k.EncryptionPubHex == "" {
		fmt.Fprintln(w, "  Encryption key   (could not be derived)")
		return
	}
	fmt.Fprintf(w, "  Encryption key   %s  (%s)\n", k.EncryptionPubHex, k.EncryptionKeyPath)
	if raw, err := hex.DecodeString(k.EncryptionPubHex); err == nil {
		fmt.Fprintf(w, "  Fingerprint      %s\n", walletderive.Fingerprint(raw))
		fmt.Fprintln(w, "  The dashboard shows this fingerprint the first time it connects; check it matches.")
	}
}

// Init runs the enrollment wizard and writes one admin row.
func Init(ctx context.Context, opts Options, term Terminal, keys ServerKeys, wallet walletderive.Wallet, store Store) (walletderive.Enrollment, error) {
	PrintServerKeys(term.Out, keys)
	fmt.Fprintln(term.Out)

	hasAdmin, err := store.HasAdmin(ctx)
	if err != nil {
		return walletderive.Enrollment{}, fmt.Errorf("check existing admins: %w", err)
	}
	if hasAdmin {
		fmt.Fprintln(term.Out, "This node already has an admin; this adds another.")
	}

	name := strings.TrimSpace(opts.Name)
	if name == "" {
		if name, err = askLine(term, "Admin display name: "); err != nil {
			return walletderive.Enrollment{}, err
		}
		if name == "" {
			return walletderive.Enrollment{}, errors.New("a display name is required")
		}
	}

	source := opts.Source
	if source == SourceAsk {
		if source, err = askSource(term); err != nil {
			return walletderive.Enrollment{}, err
		}
	}

	enrollment, err := enrollmentFor(ctx, source, opts, term, wallet)
	if err != nil {
		return walletderive.Enrollment{}, err
	}

	fmt.Fprintln(term.Out)
	fmt.Fprintln(term.Out, "New admin")
	fmt.Fprintf(term.Out, "  Name             %s\n", name)
	fmt.Fprintf(term.Out, "  Sign-in key      %s\n", enrollment.SigningPubKeyHex)
	fmt.Fprintf(term.Out, "  Account          %s\n", enrollment.RowKey)
	fmt.Fprintf(term.Out, "  Written to       %s\n", store.Describe())
	if !opts.Yes {
		ok, err := askYes(term, "Enroll this admin? [y/N] ")
		if err != nil {
			return walletderive.Enrollment{}, err
		}
		if !ok {
			return walletderive.Enrollment{}, errors.New("cancelled")
		}
	}

	if err := store.AddUser(ctx, enrollment.RowKey, name, peers.Admin, enrollment.SigningPubKeyHex); err != nil {
		return walletderive.Enrollment{}, err
	}
	fmt.Fprintln(term.Out, "Admin enrolled.")
	switch enrollment.Source {
	case walletderive.SourcePassword:
		fmt.Fprintln(term.Out, "Sign in from the dashboard with the same username and password.")
	case walletderive.SourceMnemonic:
		fmt.Fprintln(term.Out, "Sign in from the dashboard with the same recovery phrase.")
	default:
		fmt.Fprintln(term.Out, "Sign in from the dashboard with the wallet that owns this key.")
	}
	return enrollment, nil
}

func enrollmentFor(ctx context.Context, source Source, opts Options, term Terminal, wallet walletderive.Wallet) (walletderive.Enrollment, error) {
	switch source {
	case SourcePublicKey:
		pub := strings.TrimSpace(opts.PublicKey)
		if pub == "" {
			var err error
			if pub, err = askLine(term, "Sign-in public key (64 hex characters): "); err != nil {
				return walletderive.Enrollment{}, err
			}
		}
		return walletderive.FromPublicKey(pub)

	case SourcePassword:
		username := opts.Username
		if username == "" {
			var err error
			// The wallet derives from the username exactly as typed, so only
			// the line ending is removed.
			if username, err = askRaw(term, "Username: "); err != nil {
				return walletderive.Enrollment{}, err
			}
		}
		if username == "" {
			return walletderive.Enrollment{}, errors.New("a username is required")
		}
		password, err := readPassword(opts, term)
		if err != nil {
			return walletderive.Enrollment{}, err
		}
		defer zero(password)
		return walletderive.FromPassword(ctx, wallet, []byte(username), password)

	case SourceMnemonic:
		phrase, err := readSecret(opts, term, "Recovery phrase (hidden): ")
		if err != nil {
			return walletderive.Enrollment{}, err
		}
		defer zero(phrase)
		return walletderive.FromMnemonic(ctx, wallet, string(phrase))
	}
	return walletderive.Enrollment{}, fmt.Errorf("unknown source %q", source)
}

func readPassword(opts Options, term Terminal) ([]byte, error) {
	password, err := readSecret(opts, term, "Password (hidden): ")
	if err != nil {
		return nil, err
	}
	if n := utf8.RuneCount(password); n < MinPasswordChars {
		zero(password)
		return nil, fmt.Errorf("password must be at least %d characters (got %d)", MinPasswordChars, n)
	}
	if opts.SecretFromStdin {
		return password, nil
	}
	again, err := term.ReadSecret("Password again: ")
	if err != nil {
		zero(password)
		return nil, err
	}
	defer zero(again)
	if string(again) != string(password) {
		zero(password)
		return nil, errors.New("passwords do not match")
	}
	return password, nil
}

func readSecret(opts Options, term Terminal, prompt string) ([]byte, error) {
	if opts.SecretFromStdin {
		line, err := term.In.ReadString('\n')
		if err != nil && !(errors.Is(err, io.EOF) && line != "") {
			return nil, fmt.Errorf("read secret from stdin: %w", err)
		}
		return []byte(strings.TrimRight(line, "\r\n")), nil
	}
	if !term.interactive() {
		return nil, errors.New("no terminal to prompt on; pass the secret with --secret-stdin")
	}
	return term.ReadSecret(prompt)
}

func askSource(term Terminal) (Source, error) {
	fmt.Fprintln(term.Out, "How will this admin sign in?")
	fmt.Fprintln(term.Out, "  1) Username and password")
	fmt.Fprintln(term.Out, "  2) Recovery phrase (BIP-39)")
	fmt.Fprintln(term.Out, "  3) Existing wallet: paste its sign-in public key")
	answer, err := askLine(term, "Choose 1, 2 or 3: ")
	if err != nil {
		return "", err
	}
	switch answer {
	case "1":
		return SourcePassword, nil
	case "2":
		return SourceMnemonic, nil
	case "3":
		return SourcePublicKey, nil
	}
	return "", fmt.Errorf("unknown choice %q", answer)
}

func askLine(term Terminal, prompt string) (string, error) {
	line, err := askRaw(term, prompt)
	return strings.TrimSpace(line), err
}

func askRaw(term Terminal, prompt string) (string, error) {
	if !term.interactive() {
		return "", fmt.Errorf("no terminal to prompt on; missing answer for %q", strings.TrimSpace(prompt))
	}
	fmt.Fprint(term.Out, prompt)
	line, err := term.In.ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func askYes(term Terminal, prompt string) (bool, error) {
	answer, err := askLine(term, prompt)
	if err != nil {
		return false, err
	}
	answer = strings.ToLower(answer)
	return answer == "y" || answer == "yes", nil
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// AuthDBStore writes straight into the node's auth database. It is for a node
// whose daemon is not running; with the daemon up, enroll through its API.
type AuthDBStore struct {
	Users *auth.UserStore
	Path  string
}

func (s AuthDBStore) HasAdmin(context.Context) (bool, error) { return s.Users.HasAdmin(), nil }

func (s AuthDBStore) AddUser(_ context.Context, rowKey, name string, trust peers.TrustLevel, signingPubKeyHex string) error {
	if existing, err := s.Users.GetUserBySigningPubKey(signingPubKeyHex); err == nil && existing != nil {
		return fmt.Errorf("this sign-in key is already enrolled as %q", existing.Name)
	}
	return s.Users.AddUser(rowKey, name, trust, signingPubKeyHex)
}

func (s AuthDBStore) Describe() string { return "auth database " + s.Path + " (daemon not running)" }

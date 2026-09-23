package main

// REMOTE ADMIN OVER THE SEALED TRANSPORT.
//
// Since the whole-server lock (2026-09-23) a node refuses admin calls from
// another machine unless they arrive as sealed $RPC commands, so the old
// remote escape hatch (--session-token against another node) is refused with
// sealed_required. --remote replaces it for every admin command: the node's
// advertised keys are read and checked against --remote-fingerprint, the
// admin's own sign-in key is derived from a username + password or a recovery
// phrase, and every request the admin client makes is sealed to the node and
// signed by that key underneath, by the transport below.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sealed"
	"github.com/spacedatanetwork/sdn-server/internal/walletderive"
	"github.com/spacedatanetwork/sdn-server/internal/wasm"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var (
	remoteURL         string
	remoteFingerprint string
	remoteUsername    string
	remoteSecretStdin bool
)

func init() {
	pf := rootCmd.PersistentFlags()
	pf.StringVar(&remoteURL, "remote", "", "administer another node over the sealed transport (its base URL, e.g. https://sdn.example.org)")
	pf.StringVar(&remoteFingerprint, "remote-fingerprint", "", "the remote node's fingerprint, as `show-identity` or `admin init` prints it on that node (required with --remote)")
	pf.StringVar(&remoteUsername, "remote-username", "", "sign in to the remote node with this username and a password (omit to use a recovery phrase)")
	pf.BoolVar(&remoteSecretStdin, "remote-secret-stdin", false, "read the remote password or recovery phrase as one line of stdin")
}

// remoteRequested reports whether this invocation targets another node.
func remoteRequested() bool { return strings.TrimSpace(remoteURL) != "" }

// newRemoteAdminClient is newAdminClient for --remote.
func newRemoteAdminClient(cmd *cobra.Command) (*adminClient, error) {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	base := strings.TrimRight(strings.TrimSpace(remoteURL), "/")
	plain := &http.Client{Timeout: 60 * time.Second}
	node, err := sealed.ReadNodeKeys(ctx, plain, base, remoteFingerprint)
	if err != nil {
		return nil, err
	}
	signer, err := remoteSigner(ctx)
	if err != nil {
		return nil, err
	}
	return &adminClient{
		baseURL: base,
		// The sealed transport authenticates every request itself; the token
		// only keeps the client's own "have a session" checks satisfied.
		token: "sealed",
		http:  &http.Client{Timeout: 120 * time.Second, Transport: &sealedTransport{base: base, node: node, signer: signer, inner: plain}},
	}, nil
}

// remoteSigner derives the admin's sign-in key from the secret the operator
// types (or pipes). The secret is dropped as soon as the key exists.
func remoteSigner(ctx context.Context) (ed25519.PrivateKey, error) {
	wp, err := resolveHDWalletWasmPath()
	if err != nil {
		return nil, err
	}
	hw, err := wasm.NewHDWalletModule(ctx, wp)
	if err != nil {
		return nil, fmt.Errorf("failed to load HD wallet WASM: %w", err)
	}
	defer hw.Close(ctx)
	wallet := walletderive.HDWallet{HDWalletModule: hw}

	prompt := "Recovery phrase for the remote node (hidden): "
	if remoteUsername != "" {
		prompt = "Password for " + remoteUsername + " on the remote node (hidden): "
	}
	secret, err := readRemoteSecret(prompt)
	if err != nil {
		return nil, err
	}
	defer func() {
		for i := range secret {
			secret[i] = 0
		}
	}()
	var seed []byte
	if remoteUsername != "" {
		seed, err = walletderive.PasswordSeed([]byte(remoteUsername), secret)
	} else {
		seed, err = walletderive.MnemonicSeed(ctx, wallet, string(secret))
	}
	if err != nil {
		return nil, err
	}
	defer func() {
		for i := range seed {
			seed[i] = 0
		}
	}()
	return walletderive.SignInPrivateKey(ctx, wallet, seed)
}

func readRemoteSecret(prompt string) ([]byte, error) {
	if remoteSecretStdin {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && !(errors.Is(err, io.EOF) && line != "") {
			return nil, fmt.Errorf("read secret from stdin: %w", err)
		}
		return []byte(strings.TrimRight(line, "\r\n")), nil
	}
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return nil, errors.New("no terminal to prompt on; pass the secret with --remote-secret-stdin")
	}
	fmt.Fprint(os.Stderr, prompt)
	secret, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	return secret, err
}

// sealedTransport turns each admin API request into one sealed $RPC call.
type sealedTransport struct {
	base   string
	node   sealed.NodeKeys
	signer ed25519.PrivateKey
	inner  *http.Client
}

func (t *sealedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !strings.HasPrefix(req.URL.Path, "/api/") {
		return nil, fmt.Errorf("sealed transport carries /api/ requests only, not %s", req.URL.Path)
	}
	var body []byte
	if req.Body != nil {
		var err error
		body, err = io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, err
		}
	}
	route := req.URL.Path
	if req.URL.RawQuery != "" {
		route += "?" + req.URL.RawQuery
	}
	result, err := sealed.Call(req.Context(), t.inner, t.base, t.node, t.signer, req.Method, route, body, req.Header.Get("Content-Type"))
	if err != nil {
		return nil, err
	}
	header := http.Header{}
	if result.ContentType != "" {
		header.Set("Content-Type", result.ContentType)
	}
	return &http.Response{
		StatusCode:    result.Status,
		Status:        fmt.Sprintf("%d %s", result.Status, http.StatusText(result.Status)),
		Header:        header,
		Body:          io.NopCloser(bytes.NewReader(result.Body)),
		ContentLength: int64(len(result.Body)),
		Request:       req,
		Proto:         "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
	}, nil
}

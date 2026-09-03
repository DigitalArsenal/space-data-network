package main

// The authenticated admin client every CLI command uses to reach a gated
// endpoint (§19 of graph/tasks/nst-cli-full-coverage.md).
//
// # The ruling this implements
//
// The CLI does NOT bypass the auth wall. It signs in through the real
// challenge/verify admit point and reuses the session cookie the dashboard
// uses. A local short-circuit — "the CLI holds the seed, so let it act as admin
// directly" — was refused because it would be a SECOND path to Admin authority
// that issues no session, leaves no audit trail, and cannot be revoked. §14
// already decides "is this the node's root" in exactly one place, and that must
// stay true.
//
// # Why automatic sign-in, not just a token flag
//
// A token flag already existed on five command groups
// (`--session-token` / $SDN_SESSION_TOKEN). It is not a bypass, but it is close
// to unusable on the deployed shape: `sdn_wallet_session` is HttpOnly, so an
// operator cannot read it out of the page with JS; a service host has no
// browser at all; and sessions expire, making it a recurring manual ritual. So
// the flag STAYS as the remote/non-root override, and when it is absent and the
// node's seed is readable, the CLI performs the ceremony itself:
//
//	POST /api/auth/challenge  {client_pubkey_hex, ts}
//	sign the 32 raw challenge bytes with the node's root Ed25519 key
//	POST /api/auth/verify     -> Set-Cookie: sdn_wallet_session
//
// §14 admits that key as Admin because it IS the node's root key. Nothing new is
// stored, no credential is written to disk, and the sign-in shows up in the
// operator's PRR CONNECTION_COUNT like any other — which is the audit trail the
// short-circuit would have thrown away.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/keys"
	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

const sessionCookieName = "sdn_wallet_session"

// adminClient talks to the local daemon's admin API with a session.
type adminClient struct {
	baseURL string
	token   string
	http    *http.Client
	cfg     *config.Config
	res     config.Resolution
	// certPath/certServerName record the daemon certificate used as the trust
	// anchor, so a failure can name what was tried instead of only what broke.
	certPath       string
	certServerName string
}

// sessionTokenOverride reads the operator-supplied token: the --session-token
// flag if the command defines one, else $SDN_SESSION_TOKEN.
func sessionTokenOverride(cmd *cobra.Command) string {
	if cmd != nil {
		if f := cmd.Flags().Lookup("session-token"); f != nil {
			if v := strings.TrimSpace(f.Value.String()); v != "" {
				return v
			}
		}
	}
	return strings.TrimSpace(os.Getenv("SDN_SESSION_TOKEN"))
}

// newAdminClient resolves config, locates the daemon, and obtains a session.
func newAdminClient(cmd *cobra.Command) (*adminClient, error) {
	cfg, res, err := config.LoadResolved(configPath)
	if err != nil {
		return nil, err
	}
	c := &adminClient{
		baseURL: strings.TrimRight(adminURL(cfg), "/"),
		http:    &http.Client{Timeout: 60 * time.Second},
		cfg:     cfg,
		res:     res,
	}

	if token := sessionTokenOverride(cmd); token != "" {
		// REMOTE TARGET. An operator-supplied token means the CLI may be
		// pointed at another node, so verification stays on system roots —
		// the daemon-cert anchor below is for our own node only.
		c.token = token
		return c, nil
	}

	// LOCAL NODE. Anchor verification to the certificate this daemon's own
	// config declares; system roots cannot vouch for an origin/self-signed
	// cert, which is what "signed by unknown authority" was reporting.
	if tlsCfg, certPath, err := daemonTLSConfig(cfg, res); err != nil {
		return nil, err
	} else if tlsCfg != nil {
		if name := serverNameForCert(certPath, ExpectedCertHostFor(
			strings.TrimPrefix(strings.TrimPrefix(c.baseURL, "https://"), "http://"))); name != "" {
			tlsCfg.ServerName = name
		}
		c.certPath = certPath
		c.certServerName = tlsCfg.ServerName
		c.http.Transport = &http.Transport{TLSClientConfig: tlsCfg}
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	token, err := c.signInWithNodeRootKey(ctx)
	if err != nil {
		return nil, err
	}
	c.token = token
	return c, nil
}

// newAnonymousAdminClient builds a client with NO session, for the node's
// public read surface. It exists so an operator can inspect a node whose seed
// this process cannot read — the anonymous feed is anonymous for everyone, and
// a CLI that refused to read it would be inventing a gate the daemon does not
// have.
func newAnonymousAdminClient() (*adminClient, error) {
	cfg, res, err := config.LoadResolved(configPath)
	if err != nil {
		return nil, err
	}
	c := &adminClient{
		baseURL: strings.TrimRight(adminURL(cfg), "/"),
		http:    &http.Client{Timeout: 60 * time.Second},
		cfg:     cfg,
		res:     res,
	}
	if tlsCfg, certPath, terr := daemonTLSConfig(cfg, res); terr == nil && tlsCfg != nil {
		if name := serverNameForCert(certPath, ExpectedCertHostFor(
			strings.TrimPrefix(strings.TrimPrefix(c.baseURL, "https://"), "http://"))); name != "" {
			tlsCfg.ServerName = name
		}
		c.certPath = certPath
		c.certServerName = tlsCfg.ServerName
		c.http.Transport = &http.Transport{TLSClientConfig: tlsCfg}
	}
	return c, nil
}

// firstLine trims a multi-line CLI error down to its headline, for use inside a
// one-line note.
func firstLine(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return strings.TrimSpace(s[:idx])
	}
	return strings.TrimSpace(s)
}

// signInWithNodeRootKey performs the §14 root-account ceremony: load the node's
// own root key, then run the wire half.
func (c *adminClient) signInWithNodeRootKey(ctx context.Context) (string, error) {
	priv, err := loadNodeRootSigningKey(ctx, c.cfg, c.res)
	if err != nil {
		return "", err
	}
	return signInWire(ctx, c, priv)
}

// signInWire is the challenge/verify exchange, split out from key loading so the
// ceremony can be exercised against a stub daemon without a seed on disk.
func signInWire(ctx context.Context, c *adminClient, priv ed25519.PrivateKey) (string, error) {
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return "", errors.New("node root key is not Ed25519")
	}
	pubHex := hex.EncodeToString(pub)

	// 1. challenge
	var ch struct {
		ChallengeID string `json:"challenge_id"`
		Challenge   string `json:"challenge"`
	}
	body := map[string]any{"client_pubkey_hex": pubHex, "ts": time.Now().Unix()}
	if err := c.postJSON(ctx, "/api/auth/challenge", body, &ch, ""); err != nil {
		return "", fmt.Errorf("auth challenge: %w", err)
	}

	// 2. sign the RAW 32 challenge bytes — no digest, no envelope.
	//    The challenge is base64 RawStdEncoding (unpadded); it must be echoed
	//    back verbatim, so only a local copy is decoded here.
	raw, err := base64.RawStdEncoding.DecodeString(ch.Challenge)
	if err != nil {
		return "", fmt.Errorf("decode challenge: %w", err)
	}
	sig := ed25519.Sign(priv, raw)

	// 3. verify -> session cookie
	verifyBody := map[string]any{
		"challenge_id":      ch.ChallengeID,
		"client_pubkey_hex": pubHex,
		"challenge":         ch.Challenge,
		"signature_hex":     hex.EncodeToString(sig),
	}
	token, err := c.postForSessionCookie(ctx, "/api/auth/verify", verifyBody)
	if err != nil {
		return "", fmt.Errorf("auth verify: %w", err)
	}
	return token, nil
}

// loadNodeRootSigningKey derives the node's Ed25519 root signing key from its
// own seed — the same key §14 registers for root recognition.
func loadNodeRootSigningKey(ctx context.Context, cfg *config.Config, res config.Resolution) (ed25519.PrivateKey, error) {
	path := config.MnemonicPathResolved(cfg)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf(
				"%w\n  the CLI signs in as the node's root account, which needs the node's seed;\n"+
					"  pass --session-token / set SDN_SESSION_TOKEN to use an existing session instead",
				config.DescribeMissingNodeState("node identity (mnemonic)", path, res))
		}
		if os.IsPermission(err) {
			return nil, config.DescribePermissionDenied("the node mnemonic", path, keyDirOwner(config.KeyDir(cfg)), res)
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	mnemonic := string(data)
	if keys.IsMnemonicEncrypted(data) {
		password, perr := config.KeyPassword(cfg)
		if perr != nil {
			return nil, perr
		}
		if password == "" {
			password, perr = keys.DeriveDefaultPassword()
			if perr != nil {
				return nil, fmt.Errorf("derive machine-default key password: %w", perr)
			}
		}
		mnemonic, err = keys.DecryptMnemonic(data, password)
		if err != nil {
			return nil, fmt.Errorf("decrypt mnemonic (wrong password?): %w", err)
		}
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

	seed, err := hw.MnemonicToSeed(ctx, mnemonic, "")
	if err != nil {
		return nil, fmt.Errorf("derive seed: %w", err)
	}
	identity, err := hw.DeriveIdentity(ctx, seed, 0)
	if err != nil {
		return nil, fmt.Errorf("derive identity: %w", err)
	}
	rawPriv, err := identity.SigningPrivKey.Raw()
	if err != nil {
		return nil, fmt.Errorf("extract signing key: %w", err)
	}
	if len(rawPriv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("signing key is %d bytes, want %d", len(rawPriv), ed25519.PrivateKeySize)
	}
	return ed25519.PrivateKey(rawPriv), nil
}

func (c *adminClient) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// The daemon's CSRF middleware requires a same-origin Origin/Referer OR an
	// explicit X-Requested-With on cookie-authenticated state changes. A CLI has
	// no origin, so it declares itself.
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	if c.token != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: c.token})
	}
	return req, nil
}

func (c *adminClient) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := c.newRequest(ctx, method, path, reader)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: daemon unavailable at %s (config: %s)%s: %w",
			method, path, c.baseURL, c.res.Describe(),
			certHostHint(c.baseURL, err)+certAnchorHint(c.certPath, c.certServerName, err), err)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(payload)))
	}
	if out == nil {
		return nil
	}
	if len(bytes.TrimSpace(payload)) == 0 {
		return nil
	}
	return json.Unmarshal(payload, out)
}

// putRaw sends body verbatim with the given media type (FlatBuffer wires such
// as PUT /api/node/epm, which refuse JSON).
func (c *adminClient) putRaw(ctx context.Context, path, contentType string, body []byte) error {
	req, err := c.newRequest(ctx, http.MethodPut, path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("PUT %s: daemon unavailable at %s: %w", path, c.baseURL, err)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("PUT %s: %s: %s", path, resp.Status, strings.TrimSpace(string(payload)))
	}
	return nil
}

func (c *adminClient) get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

func (c *adminClient) postJSON(ctx context.Context, path string, body, out any, _ string) error {
	return c.do(ctx, http.MethodPost, path, body, out)
}

// postForSessionCookie posts and returns the issued session cookie.
func (c *adminClient) postForSessionCookie(ctx context.Context, path string, body any) (string, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := c.newRequest(ctx, http.MethodPost, path, bytes.NewReader(encoded))
	if err != nil {
		return "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("daemon unavailable at %s (config: %s): %w", c.baseURL, c.res.Describe(), err)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(payload)))
	}
	for _, cookie := range resp.Cookies() {
		if cookie.Name == sessionCookieName && cookie.Value != "" {
			return cookie.Value, nil
		}
	}
	return "", errors.New("no session cookie was issued")
}

// addSessionTokenFlag gives a command the remote/non-root override. Applied to
// every Admin-gated command so the escape hatch is uniform.
func addSessionTokenFlag(cmd *cobra.Command) {
	if cmd.Flags().Lookup("session-token") == nil {
		cmd.Flags().String("session-token", "",
			"session token for the admin API (default: $SDN_SESSION_TOKEN; omit to sign in with the node's root key)")
	}
}

// printJSON renders a value as indented JSON on stdout.
func printJSON(cmd *cobra.Command, v any) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

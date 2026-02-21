package tor

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/sha512"
	"encoding/base32"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	logging "github.com/ipfs/go-log/v2"
	"golang.org/x/crypto/sha3"
)

var log = logging.Logger("sdn-tor")

const (
	defaultBinaryPath   = "tor"
	defaultSocksAddress = "127.0.0.1:9050"
	defaultStartTimeout = 30 * time.Second
)

// StartOptions controls tor runtime startup.
type StartOptions struct {
	Enabled                 bool
	BinaryPath              string
	StoragePath             string
	DataDir                 string
	SocksAddress            string
	StartTimeout            time.Duration
	HiddenServiceEnabled    bool
	HiddenServicePort       int
	HiddenServiceTarget     string
	NodeIdentityKeyMaterial []byte
}

// Runtime holds a local tor process and derived runtime metadata.
type Runtime struct {
	cmd      *exec.Cmd
	waitDone chan error

	proxyURL  string
	onionHost string

	stopOnce sync.Once
	stopErr  error
}

// Start launches a local tor process according to the provided options.
func Start(ctx context.Context, opts StartOptions) (*Runtime, error) {
	if !opts.Enabled {
		return nil, nil
	}

	bin := strings.TrimSpace(opts.BinaryPath)
	if bin == "" {
		bin = defaultBinaryPath
	}

	socksAddr := strings.TrimSpace(opts.SocksAddress)
	if socksAddr == "" {
		socksAddr = defaultSocksAddress
	}

	startTimeout := opts.StartTimeout
	if startTimeout <= 0 {
		startTimeout = defaultStartTimeout
	}

	dataDir := strings.TrimSpace(opts.DataDir)
	if dataDir == "" {
		dataDir = defaultDataDir(opts.StoragePath)
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("create tor data dir: %w", err)
	}

	stateDir := filepath.Join(dataDir, "state")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return nil, fmt.Errorf("create tor state dir: %w", err)
	}

	lines := []string{
		"DataDirectory " + quoteTorValue(stateDir),
		"SocksPort " + socksAddr,
		"CookieAuthentication 0",
	}

	var expectedOnionHost string
	var hostnamePath string
	if opts.HiddenServiceEnabled {
		target, err := normalizeHiddenServiceTarget(opts.HiddenServiceTarget)
		if err != nil {
			return nil, err
		}

		hsDir := filepath.Join(dataDir, "hidden_service")
		if err := os.MkdirAll(hsDir, 0700); err != nil {
			return nil, fmt.Errorf("create hidden service dir: %w", err)
		}
		hostnamePath = filepath.Join(hsDir, "hostname")

		if len(opts.NodeIdentityKeyMaterial) == 0 {
			return nil, fmt.Errorf("hidden service enabled but node identity key material is empty")
		}
		derivedOnion, err := writeDeterministicHiddenServiceKeys(hsDir, opts.NodeIdentityKeyMaterial)
		if err != nil {
			return nil, err
		}
		expectedOnionHost = derivedOnion

		hsPort := opts.HiddenServicePort
		if hsPort <= 0 {
			hsPort = 80
		}

		lines = append(lines,
			"HiddenServiceDir "+quoteTorValue(hsDir),
			"HiddenServiceVersion 3",
			fmt.Sprintf("HiddenServicePort %d %s", hsPort, target),
		)
	}

	torrcPath := filepath.Join(dataDir, "torrc")
	if err := os.WriteFile(torrcPath, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		return nil, fmt.Errorf("write torrc: %w", err)
	}

	cmd := exec.CommandContext(ctx, bin, "-f", torrcPath)
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("capture tor stdout: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("capture tor stderr: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start tor process: %w", err)
	}

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- cmd.Wait()
	}()

	go streamLogs("stdout", stdoutPipe)
	go streamLogs("stderr", stderrPipe)

	cleanup := func(cause error) error {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		select {
		case <-waitDone:
		case <-time.After(2 * time.Second):
		}
		return cause
	}

	if err := waitForTCPPort(socksAddr, startTimeout); err != nil {
		return nil, cleanup(fmt.Errorf("tor socks listener not ready at %s: %w", socksAddr, err))
	}

	onionHost := expectedOnionHost
	if hostnamePath != "" {
		observedHost, err := waitForHostnameFile(hostnamePath, startTimeout)
		if err != nil {
			return nil, cleanup(fmt.Errorf("hidden service hostname not ready: %w", err))
		}
		if expectedOnionHost != "" && !strings.EqualFold(observedHost, expectedOnionHost) {
			return nil, cleanup(fmt.Errorf(
				"hidden service hostname mismatch (expected %s, got %s)",
				expectedOnionHost,
				observedHost,
			))
		}
		onionHost = observedHost
	}

	rt := &Runtime{
		cmd:       cmd,
		waitDone:  waitDone,
		proxyURL:  "socks5h://" + socksAddr,
		onionHost: onionHost,
	}

	log.Infof("TOR runtime started (socks=%s, onion=%s)", socksAddr, onionHost)
	return rt, nil
}

// Stop gracefully stops the tor process.
func (r *Runtime) Stop(ctx context.Context) error {
	if r == nil {
		return nil
	}

	r.stopOnce.Do(func() {
		if r.cmd == nil {
			return
		}

		if r.cmd.Process != nil {
			_ = r.cmd.Process.Signal(syscall.SIGTERM)
		}

		waitCh := make(chan error, 1)
		go func() {
			waitCh <- <-r.waitDone
		}()

		select {
		case err := <-waitCh:
			if err != nil {
				r.stopErr = fmt.Errorf("tor process exit: %w", err)
			}
		case <-time.After(5 * time.Second):
			if r.cmd.Process != nil {
				_ = r.cmd.Process.Kill()
			}
			if err := <-waitCh; err != nil {
				r.stopErr = fmt.Errorf("tor process forced exit: %w", err)
			}
		case <-ctx.Done():
			r.stopErr = ctx.Err()
		}
	})

	return r.stopErr
}

// ProxyURL returns the SOCKS5h proxy URL exposed by this runtime.
func (r *Runtime) ProxyURL() string {
	if r == nil {
		return ""
	}
	return r.proxyURL
}

// OnionHost returns the v3 onion hostname for this node's hidden service.
func (r *Runtime) OnionHost() string {
	if r == nil {
		return ""
	}
	return r.onionHost
}

// OnionURL returns a fully-qualified onion URL for metadata publication.
func (r *Runtime) OnionURL(useTLS bool) string {
	host := r.OnionHost()
	if host == "" {
		return ""
	}
	if useTLS {
		return "https://" + host
	}
	return "http://" + host
}

// ApplyHTTPProxy configures process-level outbound HTTP proxy settings.
func (r *Runtime) ApplyHTTPProxy(bypassLocal bool) error {
	if r == nil || r.proxyURL == "" {
		return nil
	}

	proxy := r.proxyURL
	for _, key := range []string{
		"HTTP_PROXY", "http_proxy",
		"HTTPS_PROXY", "https_proxy",
		"ALL_PROXY", "all_proxy",
	} {
		if err := os.Setenv(key, proxy); err != nil {
			return fmt.Errorf("set %s: %w", key, err)
		}
	}

	if bypassLocal {
		_ = os.Setenv("NO_PROXY", mergeNoProxy(
			os.Getenv("NO_PROXY"),
			"localhost",
			"127.0.0.1",
			"::1",
		))
		_ = os.Setenv("no_proxy", os.Getenv("NO_PROXY"))
	} else {
		_ = os.Setenv("NO_PROXY", "")
		_ = os.Setenv("no_proxy", "")
	}

	// Keep default-client users on ProxyFromEnvironment.
	if tr, ok := http.DefaultTransport.(*http.Transport); ok {
		cloned := tr.Clone()
		cloned.Proxy = http.ProxyFromEnvironment
		http.DefaultTransport = cloned
		http.DefaultClient.Transport = cloned
	}

	return nil
}

func defaultDataDir(storagePath string) string {
	if strings.TrimSpace(storagePath) == "" {
		homeDir, _ := os.UserHomeDir()
		return filepath.Join(homeDir, ".spacedatanetwork", "tor")
	}
	return filepath.Join(filepath.Dir(storagePath), "tor")
}

func quoteTorValue(v string) string {
	return strconv.Quote(v)
}

func normalizeHiddenServiceTarget(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("hidden service target is empty")
	}

	if strings.HasPrefix(raw, ":") {
		port := strings.TrimPrefix(raw, ":")
		if port == "" {
			return "", fmt.Errorf("hidden service target %q missing port", raw)
		}
		return net.JoinHostPort("127.0.0.1", port), nil
	}

	host, port, err := net.SplitHostPort(raw)
	if err != nil {
		return "", fmt.Errorf("invalid hidden service target %q: %w", raw, err)
	}
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	if port == "" {
		return "", fmt.Errorf("hidden service target %q missing port", raw)
	}
	return net.JoinHostPort(host, port), nil
}

func waitForTCPPort(addr string, timeout time.Duration) error {
	dialer := net.Dialer{Timeout: 500 * time.Millisecond}
	deadline := time.Now().Add(timeout)
	var lastErr error

	for time.Now().Before(deadline) {
		conn, err := dialer.Dial("tcp", addr)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		lastErr = err
		time.Sleep(200 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("timeout")
	}
	return lastErr
}

func waitForHostnameFile(path string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error

	for time.Now().Before(deadline) {
		host, err := readHostname(path)
		if err == nil {
			return host, nil
		}
		lastErr = err
		time.Sleep(200 * time.Millisecond)
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("timeout")
	}
	return "", lastErr
}

func readHostname(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	host := strings.TrimSpace(string(data))
	if host == "" {
		return "", fmt.Errorf("empty hostname file")
	}
	if fields := strings.Fields(host); len(fields) > 0 {
		host = fields[0]
	}
	if !strings.HasSuffix(strings.ToLower(host), ".onion") {
		return "", fmt.Errorf("invalid onion hostname %q", host)
	}
	return strings.ToLower(host), nil
}

func streamLogs(stream string, reader io.ReadCloser) {
	defer reader.Close()
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		log.Infof("tor[%s] %s", stream, line)
	}
}

func mergeNoProxy(existing string, extras ...string) string {
	seen := make(map[string]struct{})
	out := make([]string, 0, 8)

	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}

	for _, part := range strings.Split(existing, ",") {
		add(part)
	}
	for _, part := range extras {
		add(part)
	}
	return strings.Join(out, ",")
}

func writeDeterministicHiddenServiceKeys(dir string, nodeKeyMaterial []byte) (string, error) {
	seed := deriveHiddenServiceSeed(nodeKeyMaterial)
	priv := ed25519.NewKeyFromSeed(seed[:])
	pub := priv.Public().(ed25519.PublicKey)

	if err := writeHiddenServiceKeyFiles(dir, priv, pub); err != nil {
		return "", err
	}

	host, err := onionAddressFromPublicKey(pub)
	if err != nil {
		return "", err
	}
	return host, nil
}

func deriveHiddenServiceSeed(nodeKeyMaterial []byte) [32]byte {
	var seed [32]byte
	sum := sha512.Sum512(append([]byte("sdn-tor-hidden-service-v1"), nodeKeyMaterial...))
	copy(seed[:], sum[:32])
	return seed
}

func writeHiddenServiceKeyFiles(dir string, priv ed25519.PrivateKey, pub ed25519.PublicKey) error {
	if len(priv) != ed25519.PrivateKeySize {
		return fmt.Errorf("invalid ed25519 private key size: %d", len(priv))
	}
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("invalid ed25519 public key size: %d", len(pub))
	}

	secretHeader := makeTorKeyHeader("== ed25519v1-secret: type0 ==")
	publicHeader := makeTorKeyHeader("== ed25519v1-public: type0 ==")

	secretContent := append(secretHeader, priv...)
	publicContent := append(publicHeader, pub...)

	if err := os.WriteFile(filepath.Join(dir, "hs_ed25519_secret_key"), secretContent, 0600); err != nil {
		return fmt.Errorf("write hs_ed25519_secret_key: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hs_ed25519_public_key"), publicContent, 0600); err != nil {
		return fmt.Errorf("write hs_ed25519_public_key: %w", err)
	}
	return nil
}

func makeTorKeyHeader(base string) []byte {
	header := make([]byte, 32)
	copy(header, []byte(base))
	return header
}

func onionAddressFromPublicKey(pub ed25519.PublicKey) (string, error) {
	if len(pub) != ed25519.PublicKeySize {
		return "", fmt.Errorf("invalid ed25519 public key size: %d", len(pub))
	}

	version := byte(0x03)
	checksumInput := make([]byte, 0, len(".onion checksum")+len(pub)+1)
	checksumInput = append(checksumInput, []byte(".onion checksum")...)
	checksumInput = append(checksumInput, pub...)
	checksumInput = append(checksumInput, version)

	sum := sha3.Sum256(checksumInput)

	addressData := make([]byte, 0, len(pub)+2+1)
	addressData = append(addressData, pub...)
	addressData = append(addressData, sum[0], sum[1], version)

	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(addressData)
	return strings.ToLower(encoded) + ".onion", nil
}

func proxyHostPortFromEnv() map[string]struct{} {
	targets := make(map[string]struct{})
	for _, key := range []string{"ALL_PROXY", "all_proxy", "HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		raw := strings.TrimSpace(os.Getenv(key))
		if raw == "" {
			continue
		}

		parsed, err := url.Parse(raw)
		if err != nil || parsed.Host == "" {
			// Handle shorthand host:port values.
			if !strings.Contains(raw, "://") {
				if parsedAlt, altErr := url.Parse("http://" + raw); altErr == nil {
					parsed = parsedAlt
				}
			}
		}
		if parsed == nil || parsed.Host == "" {
			continue
		}
		targets[strings.ToLower(parsed.Host)] = struct{}{}
	}
	return targets
}

// AllowProxyDialTarget returns true when addr is the active proxy endpoint.
// This helper is used by SSRF-aware dialers that must permit dialing the local
// tor socks port while still rejecting arbitrary private addresses.
func AllowProxyDialTarget(addr string) bool {
	addr = strings.ToLower(strings.TrimSpace(addr))
	if addr == "" {
		return false
	}
	_, ok := proxyHostPortFromEnv()[addr]
	return ok
}

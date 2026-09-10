// Package kubo runs the node's Kubo (go-ipfs) daemon as a supervised child
// process, so a fresh install has the content-addressed store every CID,
// pin, publication and archive path needs without an operator installing
// and running Kubo by hand (INST-04 / REPL-06).
//
// The supervisor owns one repository under the node's data directory,
// initialises it on first start, binds the RPC API and the gateway to
// loopback on ports that do not collide with the node's own admin listener,
// turns the gateway's network fetch off (this node serves what it holds),
// starts `ipfs daemon`, waits for the API to answer, and restarts the child
// with backoff when it exits unexpectedly. Stop terminates the child and
// waits for it.
package kubo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Config describes the Kubo instance to supervise.
type Config struct {
	// Binary is the path to the Kubo executable (the bundle ships it under
	// runtime/kubo/ipfs).
	Binary string
	// RepoPath is the IPFS repository directory (IPFS_PATH).
	RepoPath string
	// APIAddr is the RPC API host:port on loopback. Default 127.0.0.1:5002 —
	// deliberately not Kubo's own 5001, which the node's admin listener uses.
	APIAddr string
	// GatewayAddr is the HTTP gateway host:port on loopback. Default 127.0.0.1:8080.
	GatewayAddr string
	// FetchFromNetwork leaves Gateway.NoFetch off. Default false: the gateway
	// serves only content this node holds.
	FetchFromNetwork bool
	// StartTimeout bounds the wait for the API to answer after spawn. Default 60 s.
	StartTimeout time.Duration
	// Logf receives supervisor events; nil discards them.
	Logf func(format string, args ...any)
	// Env extends the child's environment (IPFS_PATH is always set).
	Env []string
}

const (
	DefaultAPIAddr     = "127.0.0.1:5002"
	DefaultGatewayAddr = "127.0.0.1:8080"
	defaultStart       = 60 * time.Second
	maxRestartBackoff  = 30 * time.Second
)

// Health is a direct check of the supervised loopback RPC listener. Neither
// an ambient HTTP proxy nor a redirect may turn another service into Kubo.
var localRPCClient = &http.Client{
	Transport: &http.Transport{
		MaxIdleConns: 8, MaxIdleConnsPerHost: 2, IdleConnTimeout: time.Minute,
		ResponseHeaderTimeout: 3 * time.Second,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// Supervisor runs and watches one Kubo daemon.
type Supervisor struct {
	cfg Config

	mu       sync.Mutex
	cmd      *exec.Cmd
	stopping bool
	stopped  chan struct{}
	restarts int
}

// New builds a supervisor; Start does the work.
func New(cfg Config) (*Supervisor, error) {
	if strings.TrimSpace(cfg.Binary) == "" {
		return nil, errors.New("kubo: binary path is required")
	}
	if strings.TrimSpace(cfg.RepoPath) == "" {
		return nil, errors.New("kubo: repository path is required")
	}
	if cfg.APIAddr == "" {
		cfg.APIAddr = DefaultAPIAddr
	}
	if cfg.GatewayAddr == "" {
		cfg.GatewayAddr = DefaultGatewayAddr
	}
	if cfg.StartTimeout <= 0 {
		cfg.StartTimeout = defaultStart
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	for _, addr := range []string{cfg.APIAddr, cfg.GatewayAddr} {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("kubo: bad address %q: %w", addr, err)
		}
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("kubo: %q is not a loopback address; the API and gateway are bound to loopback only", addr)
		}
	}
	return &Supervisor{cfg: cfg}, nil
}

// APIURL is the RPC API base URL (what admin.ipfs_api_url should point at).
func (s *Supervisor) APIURL() string { return "http://" + s.cfg.APIAddr }

// GatewayURL is the HTTP gateway base URL (what admin.ipfs_gateway_url should point at).
func (s *Supervisor) GatewayURL() string { return "http://" + s.cfg.GatewayAddr }

// Restarts reports how many times the child was restarted after an
// unexpected exit.
func (s *Supervisor) Restarts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.restarts
}

// Start initialises the repository when it is missing, applies the address
// and gateway settings, spawns the daemon, waits until the API answers, and
// then watches the child until Stop or ctx ends.
func (s *Supervisor) Start(ctx context.Context) error {
	if err := s.ensureRepo(ctx); err != nil {
		return err
	}
	if err := s.applyConfig(ctx); err != nil {
		return err
	}
	if err := s.spawn(); err != nil {
		return err
	}
	if err := s.waitHealthy(ctx, s.cfg.StartTimeout); err != nil {
		_ = s.Stop(5 * time.Second)
		return err
	}
	s.cfg.Logf("kubo: daemon ready, API %s gateway %s repo %s", s.APIURL(), s.GatewayURL(), s.cfg.RepoPath)
	go s.watch(ctx)
	return nil
}

// Healthy reports whether the RPC API answers.
func (s *Supervisor) Healthy(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.APIURL()+"/api/v0/version", nil)
	if err != nil {
		return false
	}
	resp, err := localRPCClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var version struct{ Version string }
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<10)).Decode(&version); err != nil {
		return false
	}
	return strings.TrimSpace(version.Version) != ""
}

// Stop terminates the child (SIGTERM, then SIGKILL after timeout) and waits.
func (s *Supervisor) Stop(timeout time.Duration) error {
	s.mu.Lock()
	s.stopping = true
	cmd := s.cmd
	stopped := s.stopped
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	_ = cmd.Process.Signal(os.Interrupt)
	select {
	case <-stopped:
		return nil
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		<-stopped
		return nil
	}
}

func (s *Supervisor) env() []string {
	env := append([]string{}, os.Environ()...)
	env = append(env, "IPFS_PATH="+s.cfg.RepoPath)
	return append(env, s.cfg.Env...)
}

func (s *Supervisor) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, s.cfg.Binary, args...)
	cmd.Env = s.env()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("kubo: %s %s: %w: %s", filepath.Base(s.cfg.Binary), strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func (s *Supervisor) ensureRepo(ctx context.Context) error {
	if _, err := os.Stat(filepath.Join(s.cfg.RepoPath, "config")); err == nil {
		return nil
	}
	if err := os.MkdirAll(s.cfg.RepoPath, 0o700); err != nil {
		return fmt.Errorf("kubo: create repository directory: %w", err)
	}
	s.cfg.Logf("kubo: initialising repository at %s", s.cfg.RepoPath)
	_, err := s.run(ctx, "init", "--profile", "server", "--empty-repo")
	return err
}

func (s *Supervisor) applyConfig(ctx context.Context) error {
	settings := [][]string{
		{"config", "Addresses.API", loopbackMultiaddr(s.cfg.APIAddr)},
		{"config", "Addresses.Gateway", loopbackMultiaddr(s.cfg.GatewayAddr)},
		{"config", "--json", "Gateway.NoFetch", boolJSON(!s.cfg.FetchFromNetwork)},
	}
	for _, args := range settings {
		if _, err := s.run(ctx, args...); err != nil {
			return err
		}
	}
	return nil
}

// New has already validated the literal loopback address.
func loopbackMultiaddr(addr string) string {
	host, port, _ := net.SplitHostPort(addr)
	ip := net.ParseIP(host)
	protocol := "ip6"
	if ip.To4() != nil {
		protocol = "ip4"
	}
	return "/" + protocol + "/" + ip.String() + "/tcp/" + port
}

func boolJSON(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func (s *Supervisor) spawn() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		return errors.New("kubo: supervisor is stopping")
	}
	cmd := exec.Command(s.cfg.Binary, "daemon", "--migrate=true", "--enable-gc")
	cmd.Env = s.env()
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("kubo: start daemon: %w", err)
	}
	stopped := make(chan struct{})
	s.cmd = cmd
	s.stopped = stopped
	go func() {
		_ = cmd.Wait()
		close(stopped)
	}()
	s.cfg.Logf("kubo: daemon started (pid %d)", cmd.Process.Pid)
	return nil
}

func (s *Supervisor) waitHealthy(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if s.Healthy(ctx) {
			return nil
		}
		s.mu.Lock()
		stopped := s.stopped
		s.mu.Unlock()
		select {
		case <-stopped:
			return errors.New("kubo: daemon exited before its API answered")
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return fmt.Errorf("kubo: API at %s did not answer within %s", s.cfg.APIAddr, timeout)
}

// watch restarts the child with backoff after an unexpected exit.
func (s *Supervisor) watch(ctx context.Context) {
	backoff := time.Second
	for {
		s.mu.Lock()
		stopped := s.stopped
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			_ = s.Stop(5 * time.Second)
			return
		case <-stopped:
		}
		s.mu.Lock()
		if s.stopping {
			s.mu.Unlock()
			return
		}
		s.restarts++
		n := s.restarts
		s.mu.Unlock()
		s.cfg.Logf("kubo: daemon exited unexpectedly; restart %d in %s", n, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < maxRestartBackoff {
			backoff *= 2
			if backoff > maxRestartBackoff {
				backoff = maxRestartBackoff
			}
		}
		if err := s.spawn(); err != nil {
			s.cfg.Logf("kubo: restart failed: %v", err)
			continue
		}
		if err := s.waitHealthy(ctx, s.cfg.StartTimeout); err != nil {
			s.cfg.Logf("kubo: restarted daemon not healthy: %v", err)
			continue
		}
		backoff = time.Second
		s.cfg.Logf("kubo: daemon back (restart %d)", n)
	}
}

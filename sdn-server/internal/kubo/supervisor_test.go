package kubo

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The test binary doubles as a fake `ipfs`: `init` writes the repository's
// config file, `config` records each setting, and `daemon` serves
// /api/v0/version on the configured API address until it is signalled.
func TestMain(m *testing.M) {
	if os.Getenv("SDN_FAKE_KUBO") == "1" {
		os.Exit(fakeKubo(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func fakeKubo(args []string) int {
	repo := os.Getenv("IPFS_PATH")
	if repo == "" {
		fmt.Fprintln(os.Stderr, "IPFS_PATH not set")
		return 2
	}
	if len(args) == 0 {
		return 2
	}
	switch args[0] {
	case "init":
		return writeFile(filepath.Join(repo, "config"), "{}\n")
	case "config":
		f, err := os.OpenFile(filepath.Join(repo, "settings.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return 1
		}
		defer f.Close()
		fmt.Fprintln(f, strings.Join(args[1:], " "))
		if len(args) >= 3 && args[1] == "Addresses.API" {
			// /ip4/127.0.0.1/tcp/PORT -> 127.0.0.1:PORT
			parts := strings.Split(args[2], "/")
			if len(parts) == 5 {
				_ = writeFile(filepath.Join(repo, "api-addr"), parts[2]+":"+parts[4])
			}
		}
		return 0
	case "daemon":
		addrBytes, err := os.ReadFile(filepath.Join(repo, "api-addr"))
		if err != nil {
			return 1
		}
		if os.Getenv("SDN_FAKE_KUBO_CRASH_ONCE") == "1" {
			marker := filepath.Join(repo, "crashed-once")
			if _, err := os.Stat(marker); err != nil {
				_ = writeFile(marker, "")
				return 3
			}
		}
		ln, err := net.Listen("tcp", string(addrBytes))
		if err != nil {
			return 1
		}
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v0/version" {
				_, _ = w.Write([]byte(`{"Version":"fake"}`))
				return
			}
			http.NotFound(w, r)
		})}
		go func() { _ = srv.Serve(ln) }()
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		_ = srv.Close()
		return 0
	}
	return 2
}

func writeFile(path, content string) int {
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return 1
	}
	return 0
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func newTestSupervisor(t *testing.T, extraEnv ...string) (*Supervisor, string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(t.TempDir(), "kubo")
	sup, err := New(Config{
		Binary:       self,
		RepoPath:     repo,
		APIAddr:      freePort(t),
		GatewayAddr:  freePort(t),
		StartTimeout: 10 * time.Second,
		Env:          append([]string{"SDN_FAKE_KUBO=1"}, extraEnv...),
		Logf:         t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	return sup, repo
}

func TestSupervisorInitialisesConfiguresStartsAndStops(t *testing.T) {
	sup, repo := newTestSupervisor(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := sup.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !sup.Healthy(ctx) {
		t.Fatal("API not healthy after Start")
	}
	settings, err := os.ReadFile(filepath.Join(repo, "settings.log"))
	if err != nil {
		t.Fatalf("settings not applied: %v", err)
	}
	for _, want := range []string{"Addresses.API /ip4/127.0.0.1/tcp/", "Addresses.Gateway /ip4/127.0.0.1/tcp/", "--json Gateway.NoFetch true"} {
		if !strings.Contains(string(settings), want) {
			t.Fatalf("settings missing %q:\n%s", want, settings)
		}
	}
	if _, err := os.Stat(filepath.Join(repo, "config")); err != nil {
		t.Fatalf("repository not initialised: %v", err)
	}
	if err := sup.Stop(5 * time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if sup.Healthy(ctx) {
		t.Fatal("API still answering after Stop")
	}
	if sup.Restarts() != 0 {
		t.Fatalf("restarts = %d after a clean stop, want 0", sup.Restarts())
	}
}

func TestSupervisorRestartsACrashedDaemon(t *testing.T) {
	sup, _ := newTestSupervisor(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := sup.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer sup.Stop(5 * time.Second)

	sup.mu.Lock()
	pid := sup.cmd.Process.Pid
	sup.mu.Unlock()
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill fake daemon: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if sup.Restarts() >= 1 && sup.Healthy(ctx) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("daemon not restarted: restarts=%d healthy=%v", sup.Restarts(), sup.Healthy(ctx))
}

func TestSupervisorRefusesNonLoopbackAddresses(t *testing.T) {
	if _, err := New(Config{Binary: "ipfs", RepoPath: t.TempDir(), APIAddr: "0.0.0.0:5002"}); err == nil {
		t.Fatal("a non-loopback API address was accepted")
	}
	if _, err := New(Config{Binary: "ipfs", RepoPath: t.TempDir(), GatewayAddr: "192.0.2.1:8080"}); err == nil {
		t.Fatal("a non-loopback gateway address was accepted")
	}
}

func TestSupervisorReportsADaemonThatNeverAnswers(t *testing.T) {
	self, _ := os.Executable()
	sup, err := New(Config{
		Binary:       self,
		RepoPath:     filepath.Join(t.TempDir(), "kubo"),
		APIAddr:      freePort(t),
		GatewayAddr:  freePort(t),
		StartTimeout: 2 * time.Second,
		Env:          []string{"SDN_FAKE_KUBO=1", "SDN_FAKE_KUBO_CRASH_ONCE=1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The fake exits on its first daemon run; Start must say so instead of
	// hanging or claiming readiness.
	if err := sup.Start(context.Background()); err == nil {
		t.Fatal("Start succeeded although the daemon exited before answering")
	} else if !strings.Contains(err.Error(), "exited before") {
		t.Fatalf("Start error = %v, want the exit reported", err)
	}
	_ = exec.Command
}

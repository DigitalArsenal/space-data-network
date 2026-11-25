package main

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/update"
)

// TestUpdateHelperPublicationStopWait: what the daemon's waitBeforeStopSeconds adds to
// the helper's graceful-stop grace. No real-daemon run reaches a value other
// than the daemon's own 1800, so the arithmetic is checked here.
func TestUpdateHelperPublicationStopWait(t *testing.T) {
	for _, c := range []struct {
		seconds int64
		wait    time.Duration
		clamped bool
	}{
		{0, 0, false}, // missing (omitempty) or zero
		{-1, 0, false},
		{math.MinInt64, 0, false},
		{1, time.Second, false},
		{1800, 30 * time.Minute, false}, // the daemon's publication wait
		{3600, time.Hour, false},
		{3601, time.Hour, true},
		{1_000_000_000, time.Hour, true}, // about 31 years
		{9_300_000_000, time.Hour, true}, // overflows a Duration in seconds
		{math.MaxInt64, time.Hour, true},
	} {
		wait, clamped := publicationStopWait(c.seconds)
		if wait != c.wait || clamped != c.clamped {
			t.Errorf("publicationStopWait(%d) = %s, %t; want %s, %t", c.seconds, wait, clamped, c.wait, c.clamped)
		}
	}
}

// TestUpdateHelperStopGraceExtendsForRunningPublications verifies end to end
// that stopServingDaemon extends its graceful-stop wait by the daemon's
// waitBeforeStopSeconds so a child exiting after the base grace finishes
// cleanly without escalation, whereas without the extension the helper
// escalates once the base grace expires (stack issue 1085).
func TestUpdateHelperStopGraceExtendsForRunningPublications(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sleep child process check")
	}
	runCase := func(t *testing.T, waitSeconds int64, running int) (waitErr error, elapsed time.Duration, outText, ledgerText string) {
		t.Helper()
		child := exec.Command("sleep", "1.2")
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- child.Wait() }()
		t.Cleanup(func() {
			if child.Process != nil {
				_ = child.Process.Kill()
			}
		})

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != update.ControlShutdownPath {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"pid":                   child.Process.Pid,
				"waitBeforeStopSeconds": waitSeconds,
				"runningPublications":   running,
			})
		}))
		defer srv.Close()

		paths := update.PathsFor(t.TempDir())
		var stdout, stderr bytes.Buffer
		opts := helperApplyOptions{
			Paths:    paths,
			AdminURL: srv.URL,
			Client:   srv.Client(),
			Token:    "test-stop-grace-token",
			UpdateID: "upd-stop-grace",
			Out:      &stdout,
			Err:      &stderr,
			Stop: update.StopBounds{
				Grace:     250 * time.Millisecond,
				Escalated: 250 * time.Millisecond,
				Killed:    250 * time.Millisecond,
			},
		}
		started := time.Now()
		if _, err := opts.stopServingDaemon(context.Background(), "1.1.16"); err != nil {
			t.Fatalf("stopServingDaemon: %v (stderr=%s)", err, stderr.String())
		}
		elapsed = time.Since(started)
		waitErr = <-done
		ledgerBytes, err := os.ReadFile(filepath.Join(paths.Root, "deploy-ledger.jsonl"))
		if err != nil {
			t.Fatalf("read deploy ledger: %v", err)
		}
		return waitErr, elapsed, stdout.String(), string(ledgerBytes)
	}

	t.Run("extended_grace_waits_without_escalation", func(t *testing.T) {
		waitErr, elapsed, out, ledger := runCase(t, 4, 1)
		if waitErr != nil {
			t.Fatalf("child exited with error %v; expected clean exit during extended publication grace", waitErr)
		}
		if elapsed < time.Second {
			t.Fatalf("helper returned after %s; expected it to wait for the 1.2s child", elapsed)
		}
		if !strings.Contains(out, "daemon_stop_wait=dataset-publications running=1") {
			t.Fatalf("stdout missing publication wait notice:\n%s", out)
		}
		if !strings.Contains(ledger, "stop-waits-for-publications") {
			t.Fatalf("ledger missing stop-waits-for-publications:\n%s", ledger)
		}
		if strings.Contains(ledger, "stop-escalated") || strings.Contains(ledger, "stop-killed") {
			t.Fatalf("helper escalated or killed the daemon despite extended publication grace:\n%s", ledger)
		}
	})

	t.Run("base_grace_escalates_without_extension", func(t *testing.T) {
		waitErr, elapsed, _, ledger := runCase(t, 0, 0)
		if waitErr == nil {
			t.Fatalf("expected child to be terminated on base-grace escalation, exited cleanly after %s", elapsed)
		}
		if !strings.Contains(ledger, "stop-escalated") {
			t.Fatalf("expected stop-escalated in ledger when no publication wait was granted:\n%s", ledger)
		}
	})
}

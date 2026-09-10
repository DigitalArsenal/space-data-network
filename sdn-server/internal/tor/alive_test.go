package tor

import (
	"os/exec"
	"testing"
	"time"
)

func TestAliveTracksTheManagedProcess(t *testing.T) {
	var nilRuntime *Runtime
	if nilRuntime.Alive() {
		t.Fatal("a nil runtime must not report alive")
	}
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start a helper process: %v", err)
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	rt := &Runtime{cmd: cmd, waitDone: waitDone}
	if !rt.Alive() {
		t.Fatal("a running process must report alive")
	}
	_ = cmd.Process.Kill()
	deadline := time.Now().Add(5 * time.Second)
	for rt.Alive() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if rt.Alive() {
		t.Fatal("a killed process must stop reporting alive")
	}
	if rt.Alive() {
		t.Fatal("liveness must stay false on repeated checks")
	}
}

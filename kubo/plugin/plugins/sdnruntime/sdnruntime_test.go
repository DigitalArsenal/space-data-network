package sdnruntime

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ipfs/kubo/sdn/sdnflows"
)

func TestModuleSignaturePolicyFromTextIsFailClosedAndParsesDistinctPublishers(t *testing.T) {
	empty, err := moduleSignaturePolicyFromText("")
	if err != nil {
		t.Fatal(err)
	}
	if empty == nil || len(empty.TrustedSigners) != 0 || len(empty.AllowUnsignedByContentHash) != 0 {
		t.Fatalf("empty policy = %+v, want non-nil fail-closed policy", empty)
	}

	first := strings.Repeat("11", 32)
	second := strings.Repeat("a2", 32)
	policy, err := moduleSignaturePolicyFromText("  " + first + "," + second + "\n" + first + "  ")
	if err != nil {
		t.Fatalf("moduleSignaturePolicyFromText() error = %v", err)
	}
	if len(policy.TrustedSigners) != 2 {
		t.Fatalf("trusted signer count = %d, want 2", len(policy.TrustedSigners))
	}
	if !bytes.Equal(policy.TrustedSigners[0], bytes.Repeat([]byte{0x11}, 32)) || !bytes.Equal(policy.TrustedSigners[1], bytes.Repeat([]byte{0xa2}, 32)) {
		t.Fatalf("trusted signers = %x", policy.TrustedSigners)
	}
	if policy.AllowUnsignedByContentHash == nil {
		t.Fatal("development allowlist map is nil")
	}
}

func TestFlowInstallerBootRunsAfterDaemonStartupPathReturns(t *testing.T) {
	entered := make(chan struct{})
	resume := make(chan struct{})
	reported := make(chan error, 1)
	startFlowInstallerBoot(t.Context(), func(context.Context, []sdnflows.FlowSpec) (int, error) {
		close(entered)
		<-resume
		return 1, nil
	}, func(_ int, err error) {
		reported <- err
	})

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("background flow boot did not start")
	}
	select {
	case err := <-reported:
		t.Fatalf("flow boot reported before long activation resumed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(resume)
	select {
	case err := <-reported:
		if err != nil {
			t.Fatalf("flow boot report error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("flow boot completion was not reported")
	}
}

func TestModuleSignaturePolicyFromTextRejectsMalformedPublisher(t *testing.T) {
	for _, raw := range []string{"xyz", strings.Repeat("11", 31), strings.Repeat("11", 64)} {
		if _, err := moduleSignaturePolicyFromText(raw); err == nil {
			t.Fatalf("moduleSignaturePolicyFromText(%q) = nil error", raw)
		}
	}
}

func TestIsomorphicParentMemoryLimitSupportsBoundedLargeFramePages(t *testing.T) {
	const wasmPageBytes = uint64(64 * 1024)
	limitBytes := uint64(isomorphicParentMaxMemoryPages) * wasmPageBytes
	if limitBytes < 512*1024*1024 {
		t.Fatalf("isomorphic parent memory limit = %d bytes, want at least 512 MiB for bounded multi-frame pages", limitBytes)
	}
	if limitBytes > 1024*1024*1024 {
		t.Fatalf("isomorphic parent memory limit = %d bytes, want at most 1 GiB on an 8 GiB node", limitBytes)
	}
}

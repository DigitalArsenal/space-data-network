package versioninfo

import (
	"os"
	"strings"
	"testing"
)

// AgentVersion must be a VARIABLE, because that is the whole fix: the release
// stamp is a link-time write (-X ...versioninfo.ReleaseTag=...) into a string
// variable's data, and a const has already been folded into its uses by the
// time the linker runs. Taking its address does not compile against a const,
// so this line fails the build if anyone folds it back into the const block
// above — which is exactly how the bug lived unnoticed from 4280aaaf until
// now, silently reporting the suite version on every release binary.
var _ = &AgentVersion

func TestAgentVersionCarriesTheBuildVersion(t *testing.T) {
	if want := AgentName + "/" + Version(); AgentVersion != want {
		t.Fatalf("AgentVersion = %q, want %q — the agent string must carry the version this binary reports everywhere else", AgentVersion, want)
	}

	// The name half is what every membership check actually matches on
	// (internal/epm/sdnpeers.go isSDNAgentVersion, and the browser's
	// hasSdnIdentityEvidence). A node that loses this prefix drops off the
	// accounts board, so the version half may move but this may not.
	if !strings.HasPrefix(AgentVersion, AgentName+"/") {
		t.Fatalf("AgentVersion = %q, want the %q prefix every SDN membership check matches on", AgentVersion, AgentName+"/")
	}

	// A link-time property cannot be produced from inside the test binary, so
	// proving the stamped case end to end needs a stamped build. Set
	// SDN_TEST_EXPECT_RELEASE_TAG to the tag a release was cut as and run this
	// package's tests from that same stamped build to assert it arrived:
	//
	//	SDN_GO_LDFLAGS="-X <pkg>.ReleaseTag=v1.0.5-beta.67" \
	//	SDN_TEST_EXPECT_RELEASE_TAG=v1.0.5-beta.67 \
	//	  ./scripts/go-with-wasmedge.sh test ./internal/versioninfo/ -count=1
	//
	// Unset (the ordinary development run) this block is skipped, because an
	// unstamped build is SUPPOSED to answer with the suite version.
	expect := strings.TrimSpace(os.Getenv("SDN_TEST_EXPECT_RELEASE_TAG"))
	if expect == "" {
		return
	}
	tag := strings.TrimPrefix(expect, "v")
	if !strings.Contains(AgentVersion, tag) {
		t.Fatalf("AgentVersion = %q, want it to carry the stamped release tag %q", AgentVersion, tag)
	}
	if tag == SuiteVersion {
		t.Fatalf("SDN_TEST_EXPECT_RELEASE_TAG=%q is the bare suite version, so it cannot tell a stamped build from an unstamped one; use the release tag", expect)
	}
	if AgentVersion == AgentName+"/"+SuiteVersion {
		t.Fatalf("AgentVersion = %q: the release stamp did not reach the agent string", AgentVersion)
	}
}

func TestAgentVersionIsPinnedForTheProcess(t *testing.T) {
	// libp2p reads the user agent ONCE, at host construction, and pushes that
	// value to peers over identify. If the agent string could change after
	// start-up, two peers would hold different answers for the same node and
	// nothing would say which is current. Resolving it once at package
	// initialization is therefore deliberate, not incidental.
	before := AgentVersion

	prev := ReleaseTag
	defer func() { ReleaseTag = prev }()
	ReleaseTag = "v99.99.99-not-this-build"

	if AgentVersion != before {
		t.Fatalf("AgentVersion moved to %q after ReleaseTag changed; it must be pinned at %q for the life of the process", AgentVersion, before)
	}
}

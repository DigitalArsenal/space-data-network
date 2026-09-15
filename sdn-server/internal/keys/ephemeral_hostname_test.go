package keys

import (
	"strings"
	"testing"
)

func TestContainerDefaultHostnameShape(t *testing.T) {
	// The whole guard hangs on telling an auto-generated container hostname
	// apart from one a person chose. Too wide and it refuses correct
	// deployments; too narrow and the footgun it exists for gets through.
	for _, host := range []string{
		"3f2a91c4de70",
		"3f2a91c4de70a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f607182930",
	} {
		if !containerDefaultHostname.MatchString(host) {
			t.Errorf("container default %q not recognised", host)
		}
	}
	for _, host := range []string{
		"sdn-node-1",               // what the error message tells operators to use
		"celestrak",                // a real node in the fleet
		"sdn-0",                    // a StatefulSet pod: ephemeral-looking, actually stable
		"web-7d4b5c6f8-x2k9p",      // a Deployment pod: ephemeral, but not our call to refuse
		"3f2a91c4de7",              // 11 hex
		"3f2a91c4de701",            // 13 hex
		"3F2A91C4DE70",             // uppercase is not Docker's form
		"deadbeefcafe.example.com", // hostnameForFingerprint strips this to the label anyway
		"",
	} {
		if containerDefaultHostname.MatchString(host) {
			t.Errorf("chosen hostname %q wrongly treated as a container default", host)
		}
	}
}

func TestEphemeralHostnameHonoursOverride(t *testing.T) {
	t.Setenv(AllowEphemeralHostnameEnv, "1")
	if reason, ephemeral := EphemeralHostname(); ephemeral {
		t.Fatalf("override ignored: %s", reason)
	}
}

func TestEphemeralHostnameSealErrorIsActionable(t *testing.T) {
	// The message IS the fix for anyone who hits this: if it does not name the
	// three ways out, the operator's only remaining move is to delete the
	// volume, which is the data loss the guard exists to prevent.
	msg := EphemeralHostnameSealError("container hostname \"3f2a91c4de70\" is auto-generated").Error()
	for _, want := range []string{
		"--hostname",
		"SDN_KEY_PASSWORD_FILE",
		AllowEphemeralHostnameEnv,
		"3f2a91c4de70",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("seal refusal never mentions %q:\n%s", want, msg)
		}
	}
}

func TestEphemeralHostnameQuietOnThisMachine(t *testing.T) {
	// A developer machine is not a container; the guard must be invisible.
	if runningInContainer() {
		t.Skip("running inside a container")
	}
	if reason, ephemeral := EphemeralHostname(); ephemeral {
		t.Fatalf("false positive outside a container: %s", reason)
	}
}

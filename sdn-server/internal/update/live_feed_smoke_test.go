package update

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// LIVE FEED SMOKE TEST — opt-in, network-touching, and deliberately part of the
// tree rather than a one-off script.
//
// Everything else in this package proves the verifier is correct against bytes a
// test constructed. This proves the thing an operator actually needs to know
// before telling a fleet to update: that the manifest sitting on the live feed
// RIGHT NOW verifies with the fleet's own code, against the trust root the
// publisher advertises. Those are different claims, and only the second one is
// evidence that a release will install.
//
// Run it against the live publisher:
//
//	SDN_LIVE_FEED_URL=https://sdn.spaceaware.io/updates/cli-bundle/beta/linux/amd64 \
//	SDN_LIVE_FEED_ROOT='d4a971a7e534=MCowBQYDK2VwAyEA…' \
//	go test ./internal/update/ -run TestLiveFeedVerifies -v
//
// Skipped when unset, so it never turns a normal `go test ./...` into a network
// dependency.
func TestLiveFeedVerifies(t *testing.T) {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("SDN_LIVE_FEED_URL")), "/")
	root := strings.TrimSpace(os.Getenv("SDN_LIVE_FEED_ROOT"))
	if base == "" || root == "" {
		t.Skip("set SDN_LIVE_FEED_URL and SDN_LIVE_FEED_ROOT to smoke-test a live update feed")
	}
	keyID, publicKey, ok := strings.Cut(root, "=")
	if !ok {
		t.Fatalf("SDN_LIVE_FEED_ROOT must be key_id=base64SpkiPublicKey")
	}
	roots := TrustedRoots{keyID: publicKey}

	client := &http.Client{Timeout: 5 * time.Minute}
	fetch := func(url string) []byte {
		t.Helper()
		resp, err := client.Get(url)
		if err != nil {
			t.Fatalf("GET %s: %v", url, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: %s", url, resp.Status)
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read %s: %v", url, err)
		}
		return body
	}

	feed, err := ParseProviderFeed(fetch(base + "/index.json"))
	if err != nil {
		t.Fatalf("parse provider feed: %v", err)
	}
	selection := ProviderFeedSelection{
		Channel:         strings.Split(strings.TrimPrefix(base[strings.LastIndex(base, "/cli-bundle/")+1:], "cli-bundle/"), "/")[0],
		Platform:        "linux",
		Arch:            "amd64",
		Kind:            "cli-bundle",
		CurrentSequence: 1,
	}
	candidate, err := feed.Select(selection)
	if err != nil {
		t.Fatalf("select update: %v", err)
	}
	t.Logf("selected %s %s", candidate.UpdateID, candidate.Version)

	manifestBytes := fetch(candidate.ManifestURL)
	wasmBytes := fetch(candidate.CarrierURL)

	manifest, err := ParseManifest(manifestBytes)
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if manifest.Signing.StatementDomain == "" {
		t.Log("NOTE: the live manifest uses the LEGACY signature form (no statement_domain)")
	}

	bundleBytes, err := ExtractBundleFromCarrier(wasmBytes)
	if err != nil {
		t.Fatalf("extract bundle from carrier: %v", err)
	}

	result, err := manifest.VerifyPayload(wasmBytes, bundleBytes, VerifyOptions{
		Platform:        "linux",
		Arch:            "amd64",
		CurrentSequence: 1,
		TrustedRoots:    roots,
		Now:             time.Now(),
	})
	if err != nil {
		t.Fatalf("the live feed's payload does NOT verify: %v", err)
	}
	t.Logf("VERIFIED update_id=%s version=%s sequence=%d kind=%s bundle=%d bytes domain=%q",
		result.UpdateID, result.Version, result.Sequence, result.TargetKind, result.BundleSize,
		manifest.Signing.StatementDomain)

	// NEGATIVE CONTROL. A passing verification means nothing unless the same
	// call fails when the trust root is wrong — otherwise the test could be
	// passing because verification is not happening at all.
	if _, err := manifest.VerifyPayload(wasmBytes, bundleBytes, VerifyOptions{
		Platform:        "linux",
		Arch:            "amd64",
		CurrentSequence: 1,
		TrustedRoots:    TrustedRoots{keyID: "MCowBQYDK2VwAyEARz075WFsxi1Av0nIWFoTa/hFy8cqlDkVVC38B/aAx80="},
		Now:             time.Now(),
	}); err == nil {
		t.Fatal("negative control failed: the payload verified under a DIFFERENT trust root")
	} else {
		t.Logf("negative control ok (wrong root rejected): %v", err)
	}
}

package node

import (
	"crypto/rand"
	"encoding/base64"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/license"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
)

// Uploading a new version of a module retires its other versions after the
// grace period: the host refuses the old KEY_ID, its content_keys entry and
// host secret are gone, and its secrets lane is revoked. Driven against the
// real licensing WASM key server.

type scheduledRetirement struct {
	after time.Duration
	run   func()
}

// captureRetirements replaces the retirement scheduler for one test. Tests that
// call it must not run in parallel with each other.
func captureRetirements(t *testing.T) *[]scheduledRetirement {
	t.Helper()
	var scheduled []scheduledRetirement
	previous := scheduleLicensingRetirement
	scheduleLicensingRetirement = func(d time.Duration, f func()) {
		scheduled = append(scheduled, scheduledRetirement{after: d, run: f})
	}
	t.Cleanup(func() { scheduleLicensingRetirement = previous })
	return &scheduled
}

func uploadTestVersion(t *testing.T, reg *license.PluginRegistry, id, version string) {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.AddEncryptedPlugin(license.EncryptedPluginUpload{
		ID:                id,
		Version:           version,
		RequiredScope:     "orbpro.default",
		EncryptedBundle:   []byte("encrypted-bundle-" + version),
		KeyMaterial:       []byte(base64.RawStdEncoding.EncodeToString(key)),
		MaxGrantTimeoutMs: 30_000,
	}); err != nil {
		t.Fatalf("AddEncryptedPlugin(%s@%s): %v", id, version, err)
	}
}

func contentKeyIDs(mod *modulert.Module) map[string]bool {
	ids := map[string]bool{}
	for _, e := range licensingContentKeyEntries(mod.NodeContext().Config) {
		if entry, ok := e.(map[string]any); ok {
			if id, _ := entry["key_id"].(string); id != "" {
				ids[id] = true
			}
		}
	}
	return ids
}

func challengeIssuedFor(t *testing.T, mod *modulert.Module, requestID, moduleID, version string) bool {
	t.Helper()
	response, err := handleChallengeRequest(mod, buildChallengeRequestFrame(
		requestID, moduleID, version, "requester.orbpro.test", "localhost", "provider.orbpro.test",
	))
	if err != nil {
		t.Fatalf("server_handle_message(%s@%s): %v", moduleID, version, err)
	}
	messageType, _ := decodeChallengeHeader(t, response)
	return messageType == 1
}

func TestUploadingANewVersionRetiresTheOldOneAfterTheGrace(t *testing.T) {
	const module = "com.orbpro.core-retire-test"
	scheduled := captureRetirements(t)

	reg := writeTestPluginRegistryWithGrantPolicy(
		t,
		&license.GrantPolicyConfig{DefaultPolicy: license.GrantPolicyOpen},
		license.PluginCatalogEntry{
			ID:                module,
			Version:           "1.0.0",
			RequiredScope:     "orbpro.default",
			EncryptedPath:     "core.wasm.enc",
			KeyPath:           "core.key",
			ContentType:       "application/wasm+encrypted",
			MaxGrantTimeoutMs: 30_000,
		},
	)
	mod := newLicensingTestModule(t)
	defer mod.Close()
	if err := bootstrapLicensingModule(mod, reg); err != nil {
		t.Fatalf("bootstrapLicensingModule: %v", err)
	}
	nodeCtx := mod.NodeContext()
	const oldKeyID, newKeyID = module + ":1.0.0", module + ":2.0.0"
	oldSecret := licensingContentKeySecretPrefix + oldKeyID
	if len(*scheduled) != 0 {
		t.Fatalf("a first publication scheduled %d retirement(s)", len(*scheduled))
	}

	uploadTestVersion(t, reg, module, "2.0.0")
	if err := publishCatalogAssets(mod, reg, []string{module}); err != nil {
		t.Fatalf("publishCatalogAssets(v2): %v", err)
	}

	// One retirement, for 1.0.0, at the default grace of ten minutes.
	if len(*scheduled) != 1 || (*scheduled)[0].after != 10*time.Minute {
		t.Fatalf("scheduled retirements = %+v, want one after 10m", *scheduled)
	}

	// During the grace both releases are served.
	if ids := contentKeyIDs(mod); !ids[oldKeyID] || !ids[newKeyID] {
		t.Fatalf("content_keys during the grace = %v, want both versions", ids)
	}
	if !nodeCtx.HostSecrets.Has(oldSecret) {
		t.Fatal("the old version's host secret was dropped before the grace ran out")
	}
	if licensingDeliveryAdmission.IsKeyIDRetired(oldKeyID) {
		t.Fatal("the old version was refused before the grace ran out")
	}
	if !challengeIssuedFor(t, mod, "grace-old", module, "1.0.0") || !challengeIssuedFor(t, mod, "grace-new", module, "2.0.0") {
		t.Fatal("both versions must be challengeable during the grace")
	}

	(*scheduled)[0].run()

	if ids := contentKeyIDs(mod); ids[oldKeyID] || !ids[newKeyID] {
		t.Fatalf("content_keys after retirement = %v, want only %s", ids, newKeyID)
	}
	if nodeCtx.HostSecrets.Has(oldSecret) {
		t.Fatalf("host secret %s survived retirement", oldSecret)
	}
	if !nodeCtx.HostSecrets.Has(licensingContentKeySecretPrefix + newKeyID) {
		t.Fatal("retiring the old version dropped the new version's host secret")
	}
	if nodeCtx.CapabilityPolicy.IsApproved(mod.ContentHash(), modulert.SecretsCapabilityPrefix+oldSecret) {
		t.Fatal("the old version's secrets lane is still approved")
	}
	if !licensingDeliveryAdmission.IsKeyIDRetired(oldKeyID) {
		t.Fatal("the host still admits handshakes for the retired version")
	}
	if licensingDeliveryAdmission.IsKeyIDRetired(newKeyID) {
		t.Fatal("the current version was retired")
	}
	if !challengeIssuedFor(t, mod, "after-new", module, "2.0.0") {
		t.Fatal("the current version stopped being challengeable")
	}

	// Publishing the old version again (a rollback) reinstates it.
	uploadTestVersion(t, reg, module, "1.0.0")
	if err := publishCatalogAssets(mod, reg, []string{module}); err != nil {
		t.Fatalf("publishCatalogAssets(rollback): %v", err)
	}
	if licensingDeliveryAdmission.IsKeyIDRetired(oldKeyID) {
		t.Fatal("a version published again is still refused")
	}
	if !nodeCtx.HostSecrets.Has(oldSecret) {
		t.Fatal("a version published again has no host secret")
	}
}

// A retirement that fires after its version was published again is a no-op.
func TestRetirementOfAVersionPublishedAgainIsANoOp(t *testing.T) {
	const module = "com.orbpro.core-rollback-test"
	scheduled := captureRetirements(t)

	reg := writeTestPluginRegistryWithGrantPolicy(
		t,
		&license.GrantPolicyConfig{DefaultPolicy: license.GrantPolicyOpen},
		license.PluginCatalogEntry{
			ID:                module,
			Version:           "1.0.0",
			RequiredScope:     "orbpro.default",
			EncryptedPath:     "core.wasm.enc",
			KeyPath:           "core.key",
			ContentType:       "application/wasm+encrypted",
			MaxGrantTimeoutMs: 30_000,
		},
	)
	mod := newLicensingTestModule(t)
	defer mod.Close()
	if err := bootstrapLicensingModule(mod, reg); err != nil {
		t.Fatalf("bootstrapLicensingModule: %v", err)
	}

	uploadTestVersion(t, reg, module, "2.0.0")
	if err := publishCatalogAssets(mod, reg, []string{module}); err != nil {
		t.Fatalf("publish v2: %v", err)
	}
	uploadTestVersion(t, reg, module, "1.0.0")
	if err := publishCatalogAssets(mod, reg, []string{module}); err != nil {
		t.Fatalf("publish v1 again: %v", err)
	}
	if len(*scheduled) != 2 {
		t.Fatalf("scheduled = %d retirements, want 2 (1.0.0, then 2.0.0)", len(*scheduled))
	}
	(*scheduled)[0].run() // retires 1.0.0 — which is current again
	if !contentKeyIDs(mod)[module+":1.0.0"] || licensingDeliveryAdmission.IsKeyIDRetired(module+":1.0.0") {
		t.Fatal("a stale retirement removed the current version")
	}
	(*scheduled)[1].run() // retires 2.0.0
	if contentKeyIDs(mod)[module+":2.0.0"] || !licensingDeliveryAdmission.IsKeyIDRetired(module+":2.0.0") {
		t.Fatal("the superseded 2.0.0 was not retired")
	}
}

func TestRetireGraceIsConfigurableUpToTenMinutes(t *testing.T) {
	seconds := func(n int) *int { return &n }
	cases := []struct {
		cfg  *license.GrantPolicyConfig
		want time.Duration
	}{
		{nil, 10 * time.Minute},
		{&license.GrantPolicyConfig{}, 10 * time.Minute},
		{&license.GrantPolicyConfig{RetireSupersededAfterSeconds: seconds(0)}, 0},
		{&license.GrantPolicyConfig{RetireSupersededAfterSeconds: seconds(90)}, 90 * time.Second},
		{&license.GrantPolicyConfig{RetireSupersededAfterSeconds: seconds(600)}, 10 * time.Minute},
	}
	for _, c := range cases {
		if got := c.cfg.RetireSupersededAfter(); got != c.want {
			t.Fatalf("RetireSupersededAfter(%+v) = %s, want %s", c.cfg, got, c.want)
		}
	}
	if err := (&license.GrantPolicyConfig{RetireSupersededAfterSeconds: seconds(601)}).Validate(); err == nil {
		t.Fatal("a grace over ten minutes validated")
	}
	if err := (&license.GrantPolicyConfig{RetireSupersededAfterSeconds: seconds(-1)}).Validate(); err == nil {
		t.Fatal("a negative grace validated")
	}
}

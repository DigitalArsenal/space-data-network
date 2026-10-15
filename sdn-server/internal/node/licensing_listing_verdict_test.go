package node

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	plg "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PLG"

	"github.com/spacedatanetwork/sdn-server/internal/license"
)

// The module-delivery listings carry the admit-point verdict in $PLG fields that
// already exist, so a release gate reading only SDS bytes can prove that each
// core module@version is open and grantable:
//
//	open + grantable    LISTING_STATUS Public,  ALLOWED_XPUBS empty
//	allowlist (paid)    LISTING_STATUS Public,  ALLOWED_XPUBS listed
//	refused             LISTING_STATUS Retired
func TestListingsCarryTheGrantableVerdict(t *testing.T) {
	t.Parallel()

	const (
		openCore   = "com.orbpro.wasm-engine"
		linkKey    = "com.orbpro.gallery"
		paid       = "com.orbpro.hpop"
		refused    = "com.orbpro.unentitled"
		coreVer    = "1.4.0-core.20261015120000"
		requesterX = "xpub-test-requester"
	)
	entry := func(id, version string, xpubs ...string) license.PluginCatalogEntry {
		return license.PluginCatalogEntry{
			ID: id, Version: version, RequiredScope: "orbpro.default",
			EncryptedPath: id + ".enc", KeyPath: id + ".key",
			ContentType: "application/wasm+encrypted", MaxGrantTimeoutMs: 30_000,
			AllowedXpubs: xpubs,
		}
	}
	reg := writeTestPluginRegistryWithGrantPolicy(t,
		&license.GrantPolicyConfig{Modules: []license.GrantPolicyRule{
			{Match: openCore, Policy: license.GrantPolicyOpen},
			{Match: linkKey, Policy: license.GrantPolicyLinkKey},
		}},
		entry(openCore, coreVer),
		entry(linkKey, "1.0.0"),
		entry(paid, "2.0.0", requesterX),
		entry(refused, "1.0.0"),
	)

	listings, err := BuildModuleDeliveryListings(reg)
	if err != nil {
		t.Fatalf("BuildModuleDeliveryListings: %v", err)
	}
	byID := map[string]ModuleDeliveryListing{}
	for _, l := range listings {
		byID[l.PluginID] = l
	}

	// The fixture writes the same bytes for every encrypted bundle.
	wantHash := sha256.Sum256([]byte("encrypted-bundle"))

	cases := []struct {
		id           string
		status       string
		xpubs        int
		policy       string
		grantable    bool
		openToAnyone bool
	}{
		{refused, "Retired", 0, license.GrantPolicyAllowlist, false, false},
		{openCore, "Public", 0, license.GrantPolicyOpen, true, true},
		{linkKey, "Public", 0, license.GrantPolicyLinkKey, true, true},
		{paid, "Public", 1, license.GrantPolicyAllowlist, true, false},
	}
	for _, c := range cases {
		listing, ok := byID[c.id]
		if !ok {
			t.Fatalf("%s: not listed", c.id)
		}
		if listing.GrantPolicy != c.policy || listing.Grantable != c.grantable {
			t.Fatalf("%s: listing says policy %q grantable %v, want %q %v", c.id, listing.GrantPolicy, listing.Grantable, c.policy, c.grantable)
		}
		if !plg.PLGBufferHasIdentifier(listing.Payload) {
			t.Fatalf("%s: payload is not a $PLG", c.id)
		}
		msg := plg.GetRootAsPLG(listing.Payload, 0)
		if got := plg.EnumNamespublicationState[msg.LISTING_STATUS()]; got != c.status {
			t.Fatalf("%s: LISTING_STATUS %s, want %s", c.id, got, c.status)
		}
		if got := msg.ALLOWED_XPUBSLength(); got != c.xpubs {
			t.Fatalf("%s: %d ALLOWED_XPUBS, want %d", c.id, got, c.xpubs)
		}
		if string(msg.VERSION()) != listing.Version || string(msg.KEY_ID()) != c.id+":"+listing.Version {
			t.Fatalf("%s: VERSION %q KEY_ID %q do not name the listed version %q", c.id, msg.VERSION(), msg.KEY_ID(), listing.Version)
		}
		// What a release gate computes from the bytes alone.
		open := msg.LISTING_STATUS() == plg.EnumValuespublicationState["Public"] && msg.ALLOWED_XPUBSLength() == 0
		if open != c.openToAnyone {
			t.Fatalf("%s: a gate reading the $PLG concludes open+grantable=%v, want %v", c.id, open, c.openToAnyone)
		}
		if got := hex.EncodeToString(msg.ENCRYPTED_WASM_HASHBytes()); got != hex.EncodeToString(wantHash[:]) {
			t.Fatalf("%s: ENCRYPTED_WASM_HASH %s, want sha256 of the protected bytes %x", c.id, got, wantHash)
		}
		if msg.ENCRYPTED_WASM_SIZE() != uint64(len("encrypted-bundle")) {
			t.Fatalf("%s: ENCRYPTED_WASM_SIZE %d", c.id, msg.ENCRYPTED_WASM_SIZE())
		}
	}

	// The core release version round-trips exactly.
	if byID[openCore].Version != coreVer {
		t.Fatalf("core listing version %q, want %q", byID[openCore].Version, coreVer)
	}
}

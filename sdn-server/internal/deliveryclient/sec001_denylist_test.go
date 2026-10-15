package deliveryclient

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/trustanchors"
)

// SEC-001: a grant stamped with the published grant-verifier key is refused,
// however valid it otherwise is — anyone holding the leaked seed can mint one.
func TestGrantFromTheDenylistedVerifierIsRefused(t *testing.T) {
	g, err := DecodeGrant(encodeTestGrant(grantedFixture()))
	if err != nil {
		t.Fatal(err)
	}
	exp := GrantExpectations{
		RequestID: "req-1", ModuleID: "com.orbpro.x", ModuleVersion: "1.0.0",
		ExpectedDomain: "orbpro.default", RequestedTimeoutMs: 30_000, NowMs: 500_000,
	}
	if err := g.Validate(exp); err != nil {
		t.Fatalf("control: the fixture grant must validate: %v", err)
	}
	for _, denied := range trustanchors.Load().Denylist.Ed25519PublicKeys {
		raw, err := hex.DecodeString(denied)
		if err != nil {
			t.Fatal(err)
		}
		g.GrantVerifierPublicKey = raw
		if err := g.Validate(exp); !errors.Is(err, trustanchors.ErrDenied) {
			t.Fatalf("Validate with denylisted verifier %s = %v, want ErrDenied", denied[:8], err)
		}
	}
}

func TestRequestGrantRefusesTheDenylistedProvider(t *testing.T) {
	c, err := NewConsumer(&fakeProvider{moduleCID: "bafycid"}, testIdentity(nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, denied := range trustanchors.Load().Denylist.PeerIDs {
		if _, err := c.RequestGrant(context.Background(), Provider{PeerID: denied}, testPullParams()); !errors.Is(err, trustanchors.ErrDenied) {
			t.Fatalf("RequestGrant to the denylisted provider = %v, want ErrDenied", err)
		}
	}
}

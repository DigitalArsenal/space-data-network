package channelkeys

import (
	"bytes"
	"crypto/rand"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/ecies"
	secp256k1 "github.com/decred/dcrd/dcrec/secp256k1/v4"
	"golang.org/x/crypto/curve25519"
)

type party struct {
	id   string
	priv []byte
	pub  []byte
	kx   ecies.KeyExchange
}

func x25519Party(t *testing.T, id string) party {
	t.Helper()
	priv := make([]byte, 32)
	if _, err := rand.Read(priv); err != nil {
		t.Fatal(err)
	}
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	return party{id: id, priv: priv, pub: pub, kx: ecies.X25519}
}

func secp256k1Party(t *testing.T, id string) party {
	t.Helper()
	sk, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	return party{id: id, priv: sk.Serialize(), pub: sk.PubKey().SerializeCompressed(), kx: ecies.Secp256k1}
}

func (p party) member() Member {
	return Member{ID: p.id, PublicKey: p.pub, KeyExchange: p.kx}
}

func TestChannelWrapForMembers(t *testing.T) {
	ch, err := New("chat-room-1")
	if err != nil {
		t.Fatal(err)
	}
	if ch.Epoch() != 1 {
		t.Fatalf("initial epoch = %d, want 1", ch.Epoch())
	}
	if len(ch.ContentKey()) != contentKeyBytes {
		t.Fatalf("content key len = %d", len(ch.ContentKey()))
	}

	alice := x25519Party(t, "alice")
	bob := secp256k1Party(t, "bob")   // mixed curve
	carol := x25519Party(t, "carol")
	for _, p := range []party{alice, bob, carol} {
		if err := ch.AddMember(p.member()); err != nil {
			t.Fatalf("AddMember %s: %v", p.id, err)
		}
	}

	envs, err := ch.WrapForMembers()
	if err != nil {
		t.Fatalf("WrapForMembers: %v", err)
	}
	if len(envs) != 3 {
		t.Fatalf("got %d envelopes, want 3", len(envs))
	}

	want := ch.ContentKey()
	byID := map[string]MemberEnvelope{}
	for _, e := range envs {
		if e.Epoch != 1 {
			t.Fatalf("envelope epoch = %d, want 1", e.Epoch)
		}
		byID[e.MemberID] = e
	}
	// Every member recovers the SAME channel content key from its own envelope.
	for _, p := range []party{alice, bob, carol} {
		e, ok := byID[p.id]
		if !ok {
			t.Fatalf("no envelope for %s", p.id)
		}
		got, err := UnwrapForMember(p.priv, e.ENC, e.KMF, ch.Context())
		if err != nil {
			t.Fatalf("unwrap %s: %v", p.id, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%s recovered wrong content key", p.id)
		}
	}

	// A non-member cannot recover the content key from someone else's envelope.
	mallory := x25519Party(t, "mallory")
	if bad, err := UnwrapForMember(mallory.priv, byID["alice"].ENC, byID["alice"].KMF, ch.Context()); err == nil && bytes.Equal(bad, want) {
		t.Fatal("non-member recovered the content key")
	}
}

func TestRemoveMemberRekeysForForwardSecrecy(t *testing.T) {
	ch, err := New("chat-room-2")
	if err != nil {
		t.Fatal(err)
	}
	alice := x25519Party(t, "alice")
	bob := x25519Party(t, "bob")
	_ = ch.AddMember(alice.member())
	_ = ch.AddMember(bob.member())

	before := ch.ContentKey()
	env1, err := ch.WrapForMembers()
	if err != nil {
		t.Fatal(err)
	}
	_ = env1

	// Remove bob → content key rotates + epoch bumps.
	if err := ch.RemoveMember("bob"); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	if ch.Epoch() != 2 {
		t.Fatalf("epoch after remove = %d, want 2", ch.Epoch())
	}
	after := ch.ContentKey()
	if bytes.Equal(before, after) {
		t.Fatal("content key did not rotate on member removal")
	}

	env2, err := ch.WrapForMembers()
	if err != nil {
		t.Fatal(err)
	}
	// Only alice remains; bob has no new envelope.
	if len(env2) != 1 || env2[0].MemberID != "alice" {
		t.Fatalf("post-remove envelopes = %+v, want alice only", env2)
	}
	// Alice recovers the NEW key.
	got, err := UnwrapForMember(alice.priv, env2[0].ENC, env2[0].KMF, ch.Context())
	if err != nil || !bytes.Equal(got, after) {
		t.Fatalf("alice failed to recover rotated key: %v", err)
	}
	// Bob's OLD envelope still only yields the OLD key, never the new one —
	// bob is locked out of the new epoch.
	oldForBob := findEnv(env1, "bob")
	bobOld, err := UnwrapForMember(bob.priv, oldForBob.ENC, oldForBob.KMF, ch.Context())
	if err != nil {
		t.Fatalf("bob old-envelope unwrap: %v", err)
	}
	if bytes.Equal(bobOld, after) {
		t.Fatal("bob's old envelope recovered the rotated key — forward secrecy broken")
	}
	if !bytes.Equal(bobOld, before) {
		t.Fatal("bob's old envelope should still yield the old content key")
	}

	// Removing an absent member errors.
	if err := ch.RemoveMember("bob"); err == nil {
		t.Fatal("expected error removing absent member")
	}
}

func TestWrapForMembersGuardrails(t *testing.T) {
	ch, err := New("empty")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ch.WrapForMembers(); err == nil {
		t.Fatal("expected error wrapping for zero members")
	}
	if err := ch.AddMember(Member{ID: "", PublicKey: []byte{1}}); err == nil {
		t.Fatal("expected error for empty member id")
	}
	if err := ch.AddMember(Member{ID: "x"}); err == nil {
		t.Fatal("expected error for missing public key")
	}
	if _, err := New(""); err == nil {
		t.Fatal("expected error for empty channel id")
	}
}

func findEnv(envs []MemberEnvelope, id string) MemberEnvelope {
	for _, e := range envs {
		if e.MemberID == id {
			return e
		}
	}
	return MemberEnvelope{}
}

package peers

import (
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

type trustChangeEvent struct {
	id  peer.ID
	old TrustLevel
	new TrustLevel
}

func TestOnTrustChangeFiresOnPromotionAndDemotionViaSetTrustLevel(t *testing.T) {
	registry := NewRegistry(false, nil)
	if err := registry.AddPeer(&TrustedPeer{ID: testPeerID1, TrustLevel: Standard}); err != nil {
		t.Fatalf("AddPeer failed: %v", err)
	}

	events := make(chan trustChangeEvent, 8)
	registry.OnTrustChange(func(id peer.ID, old, newLevel TrustLevel) {
		events <- trustChangeEvent{id: id, old: old, new: newLevel}
	})

	if err := registry.SetTrustLevel(testPeerID1, Trusted); err != nil {
		t.Fatalf("SetTrustLevel(Trusted) failed: %v", err)
	}

	select {
	case ev := <-events:
		if ev.id != testPeerID1 || ev.old != Standard || ev.new != Trusted {
			t.Fatalf("promotion event = %+v, want id=%s old=Standard new=Trusted", ev, testPeerID1)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for promotion trust-change event")
	}

	// Re-asserting the same level must NOT fire a second event (dedupe).
	if err := registry.SetTrustLevel(testPeerID1, Trusted); err != nil {
		t.Fatalf("SetTrustLevel(Trusted) (no-op) failed: %v", err)
	}
	select {
	case ev := <-events:
		t.Fatalf("unexpected event on no-op trust re-assertion: %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}

	if err := registry.SetTrustLevel(testPeerID1, Standard); err != nil {
		t.Fatalf("SetTrustLevel(Standard) failed: %v", err)
	}
	select {
	case ev := <-events:
		if ev.old != Trusted || ev.new != Standard {
			t.Fatalf("demotion event = %+v, want old=Trusted new=Standard", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for demotion trust-change event")
	}
}

func TestOnTrustChangeFiresOnAddPeerAndRemovePeer(t *testing.T) {
	registry := NewRegistry(false, nil) // non-strict: unknown baseline is Standard

	events := make(chan trustChangeEvent, 8)
	registry.OnTrustChange(func(id peer.ID, old, newLevel TrustLevel) {
		events <- trustChangeEvent{id: id, old: old, new: newLevel}
	})

	if err := registry.AddPeer(&TrustedPeer{ID: testPeerID2, TrustLevel: Admin}); err != nil {
		t.Fatalf("AddPeer failed: %v", err)
	}
	select {
	case ev := <-events:
		if ev.old != Standard || ev.new != Admin {
			t.Fatalf("AddPeer event = %+v, want old=Standard (non-strict unknown baseline) new=Admin", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for AddPeer trust-change event")
	}

	if err := registry.RemovePeer(testPeerID2); err != nil {
		t.Fatalf("RemovePeer failed: %v", err)
	}
	select {
	case ev := <-events:
		if ev.old != Admin || ev.new != Standard {
			t.Fatalf("RemovePeer event = %+v, want old=Admin new=Standard (post-removal unknown baseline)", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for RemovePeer trust-change event")
	}
}

func TestOnTrustChangeFiresOnAddPeerStrictModeBaseline(t *testing.T) {
	registry := NewRegistry(true, nil) // strict: unknown baseline is Untrusted

	events := make(chan trustChangeEvent, 4)
	registry.OnTrustChange(func(id peer.ID, old, newLevel TrustLevel) {
		events <- trustChangeEvent{id: id, old: old, new: newLevel}
	})

	if err := registry.AddPeer(&TrustedPeer{ID: testPeerID3, TrustLevel: Trusted}); err != nil {
		t.Fatalf("AddPeer failed: %v", err)
	}
	select {
	case ev := <-events:
		if ev.old != Untrusted || ev.new != Trusted {
			t.Fatalf("AddPeer event = %+v, want old=Untrusted (strict unknown baseline) new=Trusted", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for AddPeer trust-change event")
	}
}

func TestOnTrustChangeFiresOnUpdatePeer(t *testing.T) {
	registry := NewRegistry(false, nil)
	if err := registry.AddPeer(&TrustedPeer{ID: testPeerID1, TrustLevel: Limited}); err != nil {
		t.Fatalf("AddPeer failed: %v", err)
	}

	events := make(chan trustChangeEvent, 4)
	registry.OnTrustChange(func(id peer.ID, old, newLevel TrustLevel) {
		events <- trustChangeEvent{id: id, old: old, new: newLevel}
	})

	if err := registry.UpdatePeer(&TrustedPeer{ID: testPeerID1, TrustLevel: Ultimate}); err != nil {
		t.Fatalf("UpdatePeer failed: %v", err)
	}
	select {
	case ev := <-events:
		if ev.old != Limited || ev.new != Ultimate {
			t.Fatalf("UpdatePeer event = %+v, want old=Limited new=Ultimate", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for UpdatePeer trust-change event")
	}
}

func TestOnTrustChangeNilHandlerIsIgnored(t *testing.T) {
	registry := NewRegistry(false, nil)
	registry.OnTrustChange(nil) // must not panic or register a nil callable

	if err := registry.AddPeer(&TrustedPeer{ID: testPeerID1, TrustLevel: Trusted}); err != nil {
		t.Fatalf("AddPeer failed: %v", err)
	}
	// If the nil handler were appended, fireTrustChange would panic
	// invoking it as a goroutine; give it a moment to surface before
	// declaring success.
	time.Sleep(50 * time.Millisecond)
}

func TestOnTrustChangeDispatchDoesNotBlockCaller(t *testing.T) {
	registry := NewRegistry(false, nil)
	if err := registry.AddPeer(&TrustedPeer{ID: testPeerID1, TrustLevel: Standard}); err != nil {
		t.Fatalf("AddPeer failed: %v", err)
	}

	release := make(chan struct{})
	registry.OnTrustChange(func(id peer.ID, old, newLevel TrustLevel) {
		<-release // blocks until the test releases it
	})

	done := make(chan error, 1)
	go func() { done <- registry.SetTrustLevel(testPeerID1, Trusted) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SetTrustLevel failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SetTrustLevel blocked on a slow trust-change handler; dispatch must be asynchronous")
	}

	close(release)
}

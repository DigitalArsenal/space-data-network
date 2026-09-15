package node

import (
	"errors"
	"fmt"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/ops"
	"github.com/spacedatanetwork/sdn-server/internal/protocol"
)

// The pubsub loop reports EVERY failed message on a schema topic. Only a
// refused dataset PUBLICATION is a publication_rejected alert; an ordinary
// record that would not validate is not, or the alert would mean nothing.
func TestPubSubPublicationAlertOnlyFiresOnPublicationRejections(t *testing.T) {
	n := &Node{alerts: ops.NewRegistry()}
	const producer = "12D3KooWaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab12cd"

	n.raisePubSubPublicationRejected(producer, errors.New("record failed schema validation"))
	if got := n.alerts.Active(); len(got) != 0 {
		t.Fatalf("Active() = %+v, want empty for an ordinary record failure", got)
	}

	rejection := fmt.Errorf("%w: %w", protocol.ErrPNMAnnouncement,
		errors.New(`SIGNATURE_TYPE "Ed25519" does not match the provider's Secp256k1 key`))
	n.raisePubSubPublicationRejected(producer, rejection)
	active := n.alerts.Active()
	if len(active) != 1 || active[0].Kind != ops.KindPublicationRejected {
		t.Fatalf("Active() = %+v, want one publication_rejected alert", active)
	}
	if active[0].Severity != ops.SeverityError {
		t.Errorf("Severity = %q, want %q", active[0].Severity, ops.SeverityError)
	}
	if active[0].Subject != shortPeerID(producer) {
		t.Errorf("Subject = %q, want %q", active[0].Subject, shortPeerID(producer))
	}
}

// A raise and its clear must agree on the subject no matter which path
// produced them: the pubsub loop, the tip queue and the stored catch-up all
// hand over a full peer id and the short form is derived in one place.
func TestPublicationAlertRaiseAndClearAgreeOnSubject(t *testing.T) {
	n := &Node{alerts: ops.NewRegistry()}
	const producer = "12D3KooWaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab12cd"

	n.raisePublicationRejected(producer, errors.New("manifest fetch failed"))
	n.raisePublicationRejected(producer, errors.New("manifest fetch failed"))
	active := n.alerts.Active()
	if len(active) != 1 || active[0].Count != 2 {
		t.Fatalf("Active() = %+v, want one alert counted twice", active)
	}

	n.clearPublicationRejected(producer)
	if got := n.alerts.Active(); len(got) != 0 {
		t.Fatalf("Active() = %+v, want empty after a publication landed", got)
	}
}

// The subject is the peer identifier the logs already print, so an operator can
// grep the alert straight back to the messages that caused it.
func TestShortPeerIDMatchesTheLoggedShortForm(t *testing.T) {
	const full = "12D3KooWaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab12cd"
	if got, want := shortPeerID(full), "12*ab12cd"; got != want {
		t.Errorf("shortPeerID = %q, want %q", got, want)
	}
	if got := shortPeerID("short"); got != "short" {
		t.Errorf("shortPeerID of a short id = %q", got)
	}
	if got := shortPeerID("  "); got != "" {
		t.Errorf("shortPeerID of blanks = %q", got)
	}
}

// A publication rejection with no error is not an alert.
func TestPublicationAlertIgnoresNilError(t *testing.T) {
	n := &Node{alerts: ops.NewRegistry()}
	n.raisePublicationRejected("12D3KooWaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab12cd", nil)
	if got := n.alerts.Active(); len(got) != 0 {
		t.Fatalf("Active() = %+v, want empty", got)
	}
}

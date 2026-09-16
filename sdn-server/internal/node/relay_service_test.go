package node

import (
	"testing"

	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
)

// Relaying by default is only safe because it is bounded. On 2026-08-08 the HOP
// service ran unconditionally with libp2p's defaults and host-01 sat at 98.5%
// CPU of 2 vCPUs with 780 inbound connections from ~700 distinct IPs while its
// real work starved. A node contributes spare capacity here; it does not exist
// to be a relay.
func TestAutoRelayResourcesAreTighterThanLibp2pDefaults(t *testing.T) {
	ours := autoRelayResources()
	theirs := relay.DefaultResources()

	if ours.MaxReservations >= theirs.MaxReservations {
		t.Errorf("MaxReservations %d is not below the libp2p default %d",
			ours.MaxReservations, theirs.MaxReservations)
	}
	if ours.MaxCircuits >= theirs.MaxCircuits {
		t.Errorf("MaxCircuits %d is not below the libp2p default %d",
			ours.MaxCircuits, theirs.MaxCircuits)
	}
	if ours.MaxReservationsPerIP >= theirs.MaxReservationsPerIP {
		t.Errorf("MaxReservationsPerIP %d is not below the libp2p default %d",
			ours.MaxReservationsPerIP, theirs.MaxReservationsPerIP)
	}
	if ours.MaxReservationsPerASN >= theirs.MaxReservationsPerASN {
		t.Errorf("MaxReservationsPerASN %d is not below the libp2p default %d",
			ours.MaxReservationsPerASN, theirs.MaxReservationsPerASN)
	}
	if ours.ReservationTTL >= theirs.ReservationTTL {
		t.Errorf("ReservationTTL %s is not below the libp2p default %s",
			ours.ReservationTTL, theirs.ReservationTTL)
	}
	// A per-connection limit must exist at all: without it a single circuit can
	// run unbounded in time and bytes.
	if ours.Limit == nil {
		t.Fatal("no per-connection RelayLimit: a relayed circuit would be unbounded")
	}
}

// Every bound must be positive; a zero would read as "unlimited" to libp2p.
func TestAutoRelayResourcesHaveNoUnlimitedFields(t *testing.T) {
	r := autoRelayResources()
	for name, value := range map[string]int{
		"MaxReservations":       r.MaxReservations,
		"MaxCircuits":           r.MaxCircuits,
		"MaxReservationsPerIP":  r.MaxReservationsPerIP,
		"MaxReservationsPerASN": r.MaxReservationsPerASN,
	} {
		if value <= 0 {
			t.Errorf("%s = %d; zero or negative reads as unlimited", name, value)
		}
	}
	if r.ReservationTTL <= 0 {
		t.Errorf("ReservationTTL = %s; must be positive", r.ReservationTTL)
	}
}

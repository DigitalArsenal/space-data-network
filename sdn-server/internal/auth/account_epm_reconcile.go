package auth

// THE FLEET LAW (owner 2026-08-28):
//
//	"All SDN nodes need to be able to pin all the EPMs that are created by
//	 accounts tied to them"
//
// A pin that was made once is not a pin that HOLDS. Stores are compacted, pin
// ledgers are rebuilt, blockstores are garbage-collected, databases are
// restored from a backup taken before the last publish, and a node that was
// offline when an account published catches up on boot. So the binding — not
// the pin — is the durable fact, and this reconciler is what makes the pin
// follow it: at boot (after the store is ready) and every six hours, every
// persisted account→epm_cid binding is checked, and anything missing is
// re-stored and re-pinned from the record bytes the node kept.
//
// It logs a WARN naming any CID it cannot satisfy. A node that cannot hold one
// of its own accounts' identities says so, by CID, rather than quietly serving
// a dangling reference.

import (
	"context"
	"strings"
	"time"
)

// accountEPMReconcileInterval is the every-six-hours cadence of the fleet law.
const accountEPMReconcileInterval = 6 * time.Hour

// AccountEPMReconciler re-pins the EPMs of every account tied to this node.
type AccountEPMReconciler struct {
	users *UserStore
	store AccountEPMStore
}

// NewAccountEPMReconciler builds the reconciler. A nil user store or an unbound
// record lane yields nil: a node with no account lane has nothing to reconcile,
// and saying so with a nil is cheaper than a goroutine that wakes every six
// hours to do nothing.
func NewAccountEPMReconciler(users *UserStore, store AccountEPMStore) *AccountEPMReconciler {
	if users == nil || store == nil {
		return nil
	}
	return &AccountEPMReconciler{users: users, store: store}
}

// AccountEPMReconcileResult reports one pass, for logs and for tests.
type AccountEPMReconcileResult struct {
	Checked     int
	Repinned    int
	Unsatisfied []string
}

// Run makes one pass over every persisted binding.
func (r *AccountEPMReconciler) Run(ctx context.Context) AccountEPMReconcileResult {
	var result AccountEPMReconcileResult
	if r == nil {
		return result
	}
	bindings, err := r.users.ListAccountEPMBindings()
	if err != nil {
		log.Warnf("Account EPM pin reconciler could not list bindings: %v", err)
		return result
	}
	for _, binding := range bindings {
		if ctx.Err() != nil {
			return result
		}
		result.Checked++

		pinned, err := r.store.AccountEPMPinned(ctx, binding.CID)
		if err != nil {
			log.Warnf("Account EPM pin reconciler could not check %s: %v", binding.CID, err)
			result.Unsatisfied = append(result.Unsatisfied, binding.CID)
			continue
		}
		if pinned {
			continue
		}

		// The bytes are the authority: re-store them and the store recomputes
		// the same content identifier, so a re-pin can never silently file the
		// record under a different CID.
		cid, err := r.store.StoreAccountEPM(ctx, AccountEPMSourceName(binding.SigningPubKeyHex), binding.EPMData)
		if err != nil {
			log.Warnf("Account EPM pin reconciler could not re-pin %s: %v", binding.CID, err)
			result.Unsatisfied = append(result.Unsatisfied, binding.CID)
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(cid), binding.CID) {
			// A different CID means the persisted bytes are not the bytes the
			// binding names. Record the new one so the next pass converges,
			// and name the old one: it is no longer held by anything.
			log.Warnf("Account EPM pin reconciler re-stored %s but the record now hashes to %s", binding.CID, cid)
			if err := r.users.SaveAccountEPM(binding.XPub, binding.EPMData, cid, binding.PhotoDataURL); err != nil {
				log.Warnf("Account EPM pin reconciler could not rebind %s: %v", binding.CID, err)
			}
			result.Unsatisfied = append(result.Unsatisfied, binding.CID)
			continue
		}
		result.Repinned++
		log.Infof("Account EPM pin reconciler re-pinned %s", cid)
	}
	return result
}

// Start runs one pass immediately, then one every six hours until ctx is done.
// The caller owns the context, so shutdown is a cancel: the ticker stops, the
// in-flight pass returns at its next binding, and the goroutine exits.
func (r *AccountEPMReconciler) Start(ctx context.Context) {
	if r == nil {
		return
	}
	go func() {
		result := r.Run(ctx)
		if result.Checked > 0 {
			log.Infof("Account EPM pin reconciler: %d binding(s) checked, %d re-pinned, %d unsatisfied",
				result.Checked, result.Repinned, len(result.Unsatisfied))
		}
		ticker := time.NewTicker(accountEPMReconcileInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.Run(ctx)
			}
		}
	}()
}

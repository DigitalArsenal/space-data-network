package api

// dataset_publication_fault.go — a failure injected into one step of a
// publication, for the real-daemon tests of what a publication that fails
// partway leaves (sdn-server/docs/publication-real-daemons.md).
//
// SDN_E2E_PUBLICATION_FAULT="announce@3" fails the announcement of the third
// window of a publication; "record completion" fails the recording of a
// publication's completion after every window was announced. The fault
// fires once per process, at the first publication that reaches it. Unset,
// nothing is injected. SDN_E2E_PUBLICATION_ALERT_CLEAR_HOLD holds the
// clearing of a publication_failed alert (heldAlertClear).

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type publicationWindowKey struct{}

// withPublicationWindow marks ctx as the publication of window part (1 for
// the first).
func withPublicationWindow(ctx context.Context, part int) context.Context {
	return context.WithValue(ctx, publicationWindowKey{}, part)
}

// publicationWindowOf is the window ctx publishes, 1 when unmarked.
func publicationWindowOf(ctx context.Context) int {
	if part, ok := ctx.Value(publicationWindowKey{}).(int); ok && part > 0 {
		return part
	}
	return 1
}

// errInjectedPublicationFault is the error of an injected fault.
var errInjectedPublicationFault = errors.New("injected publication fault (SDN_E2E_PUBLICATION_FAULT)")

var (
	publicationFaultOnce   sync.Once
	publicationFaultStep   string
	publicationFaultWindow int
	publicationFaultFired  atomic.Bool
)

var (
	alertClearHoldOnce sync.Once
	alertClearHold     time.Duration
)

// heldAlertClear holds the clearing of a lane's publication_failed alert for
// SDN_E2E_PUBLICATION_ALERT_CLEAR_HOLD (a duration; unset, nothing is held),
// after it was decided and before it is made, for the real-daemon test of a
// failure raised meanwhile. The caller holds what makes the decision and the
// clear one step.
func heldAlertClear(lane string) {
	alertClearHoldOnce.Do(func() {
		spec := strings.TrimSpace(os.Getenv("SDN_E2E_PUBLICATION_ALERT_CLEAR_HOLD"))
		if spec == "" {
			return
		}
		hold, err := time.ParseDuration(spec)
		if err != nil || hold <= 0 {
			log.Errorf("SDN_E2E_PUBLICATION_ALERT_CLEAR_HOLD %q: not a positive duration; nothing is held", spec)
			return
		}
		alertClearHold = hold
	})
	if alertClearHold <= 0 {
		return
	}
	log.Warnf("SDN_E2E_PUBLICATION_ALERT_CLEAR_HOLD: the clearing of the publication_failed alert of %s is held %s", lane, alertClearHold)
	time.Sleep(alertClearHold)
}

// injectedPublicationFault answers the injected fault of step, at the window
// ctx publishes, once; nil otherwise.
func injectedPublicationFault(ctx context.Context, step string) error {
	publicationFaultOnce.Do(func() {
		spec := strings.TrimSpace(os.Getenv("SDN_E2E_PUBLICATION_FAULT"))
		if spec == "" {
			return
		}
		name, window, found := strings.Cut(spec, "@")
		publicationFaultStep, publicationFaultWindow = strings.TrimSpace(name), 0
		if found {
			n, err := strconv.Atoi(strings.TrimSpace(window))
			if err != nil || n < 1 {
				log.Errorf("SDN_E2E_PUBLICATION_FAULT %q: the window must be a positive number; no fault is injected", spec)
				publicationFaultStep = ""
				return
			}
			publicationFaultWindow = n
		}
		log.Warnf("SDN_E2E_PUBLICATION_FAULT: the first publication that reaches step %q (window %d; 0: any) fails", publicationFaultStep, publicationFaultWindow)
	})
	if publicationFaultStep != step {
		return nil
	}
	window := publicationWindowOf(ctx)
	if publicationFaultWindow > 0 && window != publicationFaultWindow {
		return nil
	}
	if !publicationFaultFired.CompareAndSwap(false, true) {
		return nil
	}
	return fmt.Errorf("window %d: %w", window, errInjectedPublicationFault)
}

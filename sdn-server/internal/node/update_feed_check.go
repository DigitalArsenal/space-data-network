package node

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/ops"
	"github.com/spacedatanetwork/sdn-server/internal/update"
)

// A check tries at most three candidates to bound work on a broken index.
// Candidates beyond this budget wait for a later check.
const feedCandidateLimit = 3

// Three unsuccessful checks raise a warning; a clean check clears it.
const feedFailureAlertThreshold = 3

func resolvedCheckInterval(seconds int) time.Duration {
	if seconds <= 0 {
		return 6 * time.Hour
	}
	return time.Duration(seconds) * time.Second
}

func (s *UpdateSignalSubscriber) runFeedChecks(ctx context.Context) {
	delay := s.deps.CheckDelay
	if delay <= 0 {
		delay = time.Minute + time.Duration(rand.Int63n(int64(4*time.Minute)))
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	alerts := time.NewTicker(5 * time.Second)
	defer alerts.Stop()
	consecutiveFailures := 0
	consecutiveInstallFailures := 0
	installAlertID := "feed-check:" + s.deps.Channel
	for {
		select {
		case <-ctx.Done():
			return
		case <-alerts.C:
			s.restoreFailedAlerts()
		case <-timer.C:
			err := s.checkFeed(ctx)
			var unavailable *feedReadError
			if errors.Is(err, errFeedCheckDeferred) || errors.Is(err, errFeedCheckHandedOff) {
				// Deferral or helper launch provides no evidence of a completed installation.
			} else if errors.As(err, &unavailable) {
				s.reportFailure("feed-check", "", err)
				consecutiveInstallFailures = 0
				consecutiveFailures++
				if consecutiveFailures >= feedFailureAlertThreshold {
					s.deps.Alerts.Raise(ops.KindUpdateFeedUnavailable, s.deps.Channel, ops.SeverityWarning, "Update feed cannot be read: "+err.Error())
				}
			} else if err != nil {
				consecutiveInstallFailures++
				var entryFailure *feedInstallError
				if !errors.As(err, &entryFailure) {
					s.reportFailure("feed-install", "", err)
				}
				if consecutiveInstallFailures >= feedFailureAlertThreshold {
					s.deps.Alerts.Raise(ops.KindUpdateFailed, installAlertID, ops.SeverityWarning, "Update feed installation failed: "+err.Error())
				}
			} else {
				consecutiveInstallFailures = 0
				s.deps.Alerts.Clear(ops.KindUpdateFailed, installAlertID)
				consecutiveFailures = 0
				s.deps.Alerts.Clear(ops.KindUpdateFeedUnavailable, s.deps.Channel)
				s.mu.Lock()
				for cause := range s.reportedFailures {
					if strings.HasPrefix(cause, "feed-check:") {
						delete(s.reportedFailures, cause)
					}
				}
				s.mu.Unlock()
			}
			interval := s.deps.CheckInterval
			offset := time.Duration(rand.Int63n(int64(interval/5) + 1))
			timer.Reset(interval + offset)
		}
	}
}

var errFeedCheckDeferred = errors.New("feed check deferred by an active update")
var errFeedCheckHandedOff = errors.New("feed check handed installation to the helper")

func (s *UpdateSignalSubscriber) checkFeed(ctx context.Context) (resultErr error) {
	if !s.claimFeed() {
		return errFeedCheckDeferred
	}
	succeeded := false
	updateID := ""
	var lastInstallErr error
	hadInstallFailure := false
	defer func() {
		if resultErr == nil && !hadInstallFailure {
			s.mu.Lock()
			for cause := range s.reportedFailures {
				if strings.HasPrefix(cause, "feed-install:") {
					delete(s.reportedFailures, cause)
				}
			}
			s.mu.Unlock()
		}
	}()
	operationCtx := ctx
	defer func() { s.finishUpgrade(operationCtx, succeeded, updateID) }()
	ctx, cancel := context.WithTimeout(ctx, signalFetchTimeout)
	defer cancel()
	state, err := update.LoadState(s.deps.Paths)
	if err != nil {
		return err
	}
	indexURL, err := update.ProviderFeedIndexURL(s.feed.String(), s.deps.Channel, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	raw, err := s.fetch(ctx, indexURL, 16<<20)
	if err != nil {
		return &feedReadError{operation: "fetch update index", err: err}
	}
	feed, err := update.ParseProviderFeed(raw)
	if err != nil {
		return &feedReadError{operation: "parse update index", err: err}
	}
	excluded := s.feedDeferredCandidates
	if excluded == nil {
		excluded = map[string]bool{}
	}
	s.feedDeferredCandidates = excluded
	for attempted := 0; attempted < feedCandidateLimit; attempted++ {
		entry, err := feed.Select(update.ProviderFeedSelection{
			Exclude: func(entry update.ProviderFeedUpdate) bool {
				return excluded[entry.UpdateID] || update.HasFailedUpdate(s.deps.Paths, entry.UpdateID)
			},
			Channel: s.deps.Channel, Platform: runtime.GOOS, Arch: runtime.GOARCH, Kind: s.deps.Kind, CurrentSequence: state.Sequence,
		})
		if errors.Is(err, update.ErrNoCompatibleUpdate) {
			if attempted == 0 && len(excluded) > 0 {
				excluded = map[string]bool{}
				s.feedDeferredCandidates = excluded
				attempted--
				continue
			}
			s.feedDeferredCandidates = nil
			return lastInstallErr
		}
		if err != nil {
			return err
		}
		excluded[entry.UpdateID] = true
		updateID = entry.UpdateID
		if err := entry.AssertUnderFeed(s.feed); err != nil {
			hadInstallFailure = true
			lastInstallErr = &feedInstallError{id: entry.UpdateID, err: err}
			s.reportFailure("feed-install", entry.UpdateID, err)
			continue
		}
		s.mu.Lock()
		claimed := s.handled[entry.UpdateID]
		previousRefusal := s.installRefusals[entry.UpdateID]
		if !claimed {
			s.handled[entry.UpdateID] = true
		}
		s.mu.Unlock()
		if claimed {
			if previousRefusal != nil {
				hadInstallFailure = true
				lastInstallErr = previousRefusal
			}
			log.Debugf("feed update %s dropped: already handled", entry.UpdateID)
			continue
		}
		signal := update.SignalFromFeedEntry(*entry, s.feed.String(), s.deps.Now(), false)
		err = s.upgradeTriggered(ctx, signal, state, "feed-check")
		if err == nil {
			succeeded = true
			s.feedDeferredCandidates = nil
			return errFeedCheckHandedOff
		}
		var refusal *update.StoreFormatRefusal
		if !errors.As(err, &refusal) {
			s.release(entry.UpdateID)
		}
		lastInstallErr = &feedInstallError{id: entry.UpdateID, err: err}
		if refusal != nil {
			s.mu.Lock()
			if s.installRefusals == nil {
				s.installRefusals = make(map[string]error)
			}
			s.installRefusals[entry.UpdateID] = lastInstallErr
			s.mu.Unlock()
		}
		hadInstallFailure = true
		s.reportFailure("feed-install", entry.UpdateID, err)
	}
	return lastInstallErr
}

// Entry refusals are logged by candidate identity, once per cause.
type feedInstallError struct {
	id  string
	err error
}

func (e *feedInstallError) Error() string { return "update " + e.id + ": " + e.err.Error() }
func (e *feedInstallError) Unwrap() error { return e.err }

type feedReadError struct {
	operation string
	err       error
}

func (e *feedReadError) Error() string { return e.operation + ": " + e.err.Error() }
func (e *feedReadError) Unwrap() error { return e.err }

// claimFeed reserves the install without charging the swap interval for a download.
func (s *UpdateSignalSubscriber) claimFeed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pendingUpgrade {
		log.Debug("feed check deferred: update in flight")
		return false
	}
	if s.deps.Now().Before(s.swapAllowedAtLocked()) {
		log.Debug("feed check deferred: minimum upgrade interval or hand-off retry backoff")
		return false
	}
	s.pendingUpgrade = true
	return true
}

// The helper has one health budget plus all three stop-ladder bounds to take over.
// With the default 600-second health budget this is 730 seconds.
func (s *UpdateSignalSubscriber) handoffTimeout() time.Duration {
	health := s.deps.HealthTimeout
	if health <= 0 {
		health = 600 * time.Second
	}
	return health + update.DefaultStopGrace + update.DefaultStopEscalated + update.DefaultStopKilled
}

func (s *UpdateSignalSubscriber) finishUpgrade(ctx context.Context, succeeded bool, updateID string) {
	s.mu.Lock()
	if succeeded {
		s.lastUpgrade = s.deps.Now()
		s.mu.Unlock()
		go func() {
			timer := time.NewTimer(s.handoffTimeout())
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			s.handoffTimedOut(ctx, updateID)
		}()
		return
	}
	// Keep the newest announcement reserved across the rate limit. A replay
	// that fails can receive another announcement without losing it to the floor.
	if s.pendingSignal != nil && ctx.Err() == nil {
		delay := s.swapAllowedAtLocked().Sub(s.deps.Now())
		if delay > 0 {
			log.Debugf("remembered update signal deferred for minimum upgrade interval: %s", delay)
			s.mu.Unlock()
			go func() {
				timer := time.NewTimer(delay)
				defer timer.Stop()
				select {
				case <-ctx.Done():
					return
				case <-timer.C:
					s.finishUpgrade(ctx, false, "")
				}
			}()
			return
		}
	}
	queued := s.pendingSignal
	s.pendingSignal, s.pendingSequence = nil, 0
	s.pendingUpgrade = false
	s.mu.Unlock()
	if queued != nil && ctx.Err() == nil {
		s.handle(ctx, queued)
	}
}

// The first retry after a failed hand-off waits one minimum interval; each
// further consecutive failure doubles the wait, up to this bound.
const maxHandoffBackoff = 24 * time.Hour

func handoffBackoff(base time.Duration, failures int) time.Duration {
	if base <= 0 {
		base = time.Second
	}
	backoff := base
	for i := 1; i < failures && backoff < maxHandoffBackoff; i++ {
		backoff *= 2
	}
	return min(backoff, maxHandoffBackoff)
}

// swapAllowedAtLocked is the earliest time another swap may start: the minimum
// interval after the last swap, or the end of a hand-off retry backoff. The
// caller holds s.mu.
func (s *UpdateSignalSubscriber) swapAllowedAtLocked() time.Time {
	var at time.Time
	if !s.lastUpgrade.IsZero() {
		at = s.lastUpgrade.Add(s.deps.MinInterval)
	}
	if s.handoffRetryAt.After(at) {
		at = s.handoffRetryAt
	}
	return at
}

// handoffTimedOut runs when this daemon is still serving after the hand-off
// bound. A daemon that accepted the update shutdown has stopped by then, so
// the helper never took over. Unless the helper applied the update or
// quarantined it itself, nothing was swapped: the same update is retried after
// a backoff that applies to every update, so a cause that repeats neither
// locks this release out nor moves the feed check on to older releases.
func (s *UpdateSignalSubscriber) handoffTimedOut(ctx context.Context, updateID string) {
	err := fmt.Errorf("update %s: helper did not take over within %s; this daemon is still serving", updateID, s.handoffTimeout())
	installed := update.HasFailedUpdate(s.deps.Paths, updateID)
	if state, stateErr := update.LoadState(s.deps.Paths); stateErr == nil && state.UpdateID == updateID {
		installed = true
	}
	if installed {
		// The helper applied or gated this update: keep the quarantine, which
		// only an operator clears.
		if quarantineErr := os.MkdirAll(filepath.Join(s.deps.Paths.Failed, updateID), 0o700); quarantineErr != nil {
			err = fmt.Errorf("%w; quarantine failed: %v", err, quarantineErr)
		}
	} else {
		s.mu.Lock()
		s.handoffFailures++
		backoff := handoffBackoff(s.deps.MinInterval, s.handoffFailures)
		s.handoffRetryAt = s.deps.Now().Add(backoff)
		failures := s.handoffFailures
		s.mu.Unlock()
		err = fmt.Errorf("%w; nothing was swapped; hand-off failure %d, next attempt after %s", err, failures, backoff)
	}
	if ledgerErr := update.RecordDeployLedgerEntry(s.deps.Paths, update.DeployLedgerEntry{
		Action: "handoff-failed", UpdateID: updateID, Reason: err.Error(),
	}); ledgerErr != nil {
		log.Warnf("Record failed update handoff: %v", ledgerErr)
	}
	s.reportFailure("handoff", updateID, err)
	s.deps.Alerts.Raise(ops.KindUpdateFailed, updateID, ops.SeverityError, err.Error())
	s.release(updateID)
	s.finishUpgrade(ctx, false, "")
}

func (s *UpdateSignalSubscriber) reportFailure(trigger, id string, err error) {
	cause := trigger + ":" + id + ":" + err.Error()
	s.mu.Lock()
	reported := s.reportedFailures[cause]
	s.reportedFailures[cause] = true
	s.mu.Unlock()
	if !reported {
		log.Warnf("Update %s %s failed: %v", trigger, id, err)
	}
}

// restoreFailedAlerts reconciles health-gate reversals with subsequent installations.
func (s *UpdateSignalSubscriber) restoreFailedAlerts() {
	path := filepath.Join(s.deps.Paths.Root, "deploy-ledger.jsonl")
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		log.Warnf("Read update ledger: %v", err)
		return
	}
	stateInfo, stateErr := os.Stat(s.deps.Paths.State)
	stamp := fmt.Sprintf("%v/%v/%v/%v/%v", info.ModTime(), info.Size(), info.Mode(), stateInfo, stateErr)
	if stamp == s.alertFilesStamp {
		return
	}
	// Cache failures too; a file change permits a fresh attempt.
	s.alertFilesStamp = stamp
	file, err := os.Open(path)
	if err != nil {
		log.Warnf("Read update ledger: %v", err)
		return
	}
	defer file.Close()
	failures := map[string]update.DeployLedgerEntry{}
	reader := bufio.NewReaderSize(file, 64<<10)
	for {
		var line []byte
		oversized := false
		var readErr error
		for {
			fragment, more, err := reader.ReadLine()
			readErr = err
			if len(line)+len(fragment) > 4<<20 {
				oversized = true
				line = nil
			}
			if !oversized {
				line = append(line, fragment...)
			}
			if !more || err != nil {
				break
			}
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			log.Warnf("Read update ledger: %v", readErr)
			return
		}
		var entry update.DeployLedgerEntry
		if !oversized && json.Unmarshal(line, &entry) == nil {
			switch entry.Action {
			case "health-reverted":
				failures[entry.UpdateID] = entry
			case "health-passed":
				if failed, ok := failures[entry.UpdateID]; ok && failed.Version == entry.Version && entry.Sequence >= failed.Sequence {
					delete(failures, entry.UpdateID)
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	state, err := update.LoadState(s.deps.Paths)
	if err != nil {
		log.Warnf("Read update state: %v", err)
		return
	}
	active := map[string]bool{}
	for id, failed := range failures {
		if state.Sequence >= failed.Sequence {
			continue
		}
		active[id] = true
		if !s.failedAlerts[id] {
			s.deps.Alerts.Raise(ops.KindUpdateFailed, id, ops.SeverityError,
				fmt.Sprintf("Update %s (%s) failed its health gate and was rolled back at %s: %s", id, failed.Version, failed.RecordedAt, failed.Reason))
		}
	}
	for id := range s.failedAlerts {
		if !active[id] {
			s.deps.Alerts.Clear(ops.KindUpdateFailed, id)
		}
	}
	s.failedAlerts = active
	s.alertLedgerTime, s.alertLedgerSize = info.ModTime(), info.Size()
}

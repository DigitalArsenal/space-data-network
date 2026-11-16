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
	for {
		select {
		case <-ctx.Done():
			return
		case <-alerts.C:
			s.restoreFailedAlerts()
		case <-timer.C:
			err := s.checkFeed(ctx)
			var unavailable *feedReadError
			if errors.Is(err, errFeedCheckDeferred) {
				// A deferred check provides no evidence of feed recovery.
			} else if errors.As(err, &unavailable) {
				s.reportFailure("feed-check", "", err)
				consecutiveFailures++
				if consecutiveFailures >= 3 {
					s.deps.Alerts.Raise(ops.KindUpdateFeedUnavailable, s.deps.Channel, ops.SeverityWarning, "Update feed cannot be read: "+err.Error())
				}
			} else {
				consecutiveFailures = 0
				s.deps.Alerts.Clear(ops.KindUpdateFeedUnavailable, s.deps.Channel)
				if err != nil {
					s.reportFailure("feed-install", "", err)
				}
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

func (s *UpdateSignalSubscriber) checkFeed(ctx context.Context) (resultErr error) {
	if !s.claimFeed() {
		return errFeedCheckDeferred
	}
	succeeded := false
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
	defer func() { s.finishUpgrade(operationCtx, succeeded) }()
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
	excluded := map[string]bool{}
	for {
		entry, err := feed.Select(update.ProviderFeedSelection{
			Exclude: func(entry update.ProviderFeedUpdate) bool {
				return excluded[entry.UpdateID] || update.HasFailedUpdate(s.deps.Paths, entry.UpdateID)
			},
			Channel: s.deps.Channel, Platform: runtime.GOOS, Arch: runtime.GOARCH, Kind: s.deps.Kind, CurrentSequence: state.Sequence,
		})
		if errors.Is(err, update.ErrNoCompatibleUpdate) {
			return nil
		}
		if err != nil {
			return err
		}
		excluded[entry.UpdateID] = true
		if err := entry.AssertUnderFeed(s.feed); err != nil {
			hadInstallFailure = true
			s.reportFailure("feed-install", entry.UpdateID, err)
			continue
		}
		s.mu.Lock()
		claimed := s.handled[entry.UpdateID]
		if !claimed {
			s.handled[entry.UpdateID] = true
		}
		s.mu.Unlock()
		if claimed {
			log.Debugf("feed update %s dropped: already handled", entry.UpdateID)
			continue
		}
		signal := update.SignalFromFeedEntry(*entry, s.feed.String(), s.deps.Now(), false)
		err = s.upgradeTriggered(ctx, signal, state, "feed-check")
		if err == nil {
			succeeded = true
			return nil
		}
		var refusal *update.StoreFormatRefusal
		if !errors.As(err, &refusal) {
			s.release(entry.UpdateID)
		}
		var unavailable *feedReadError
		if errors.As(err, &unavailable) {
			return err
		}
		hadInstallFailure = true
		s.reportFailure("feed-install", entry.UpdateID, err)
	}
}

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
	if !s.lastUpgrade.IsZero() && s.deps.Now().Sub(s.lastUpgrade) < s.deps.MinInterval {
		log.Debug("feed check deferred: minimum upgrade interval")
		return false
	}
	s.pendingUpgrade = true
	return true
}

func (s *UpdateSignalSubscriber) finishUpgrade(ctx context.Context, succeeded bool) {
	s.mu.Lock()
	queued := s.pendingSignal
	s.pendingSignal, s.pendingSequence = nil, 0
	if succeeded {
		// The helper owns the swap; no concurrent work starts before this daemon exits.
		s.lastUpgrade = s.deps.Now()
		if queued != nil {
			log.Debug("update signal deferred to catch-up after helper handoff")
		}
		s.mu.Unlock()
		return
	}
	s.pendingUpgrade = false
	s.mu.Unlock()
	if queued != nil && ctx.Err() == nil {
		s.handle(ctx, queued)
	}
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

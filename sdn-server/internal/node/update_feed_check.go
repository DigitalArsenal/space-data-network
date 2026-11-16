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
			if err := s.checkFeed(ctx); err != nil {
				s.reportFailure("feed-check", "", err)
				consecutiveFailures++
				if consecutiveFailures >= 3 {
					s.deps.Alerts.Raise(ops.KindUpdateFeedUnavailable, s.deps.Channel, ops.SeverityWarning, "Update feed cannot be read: "+err.Error())
				}
			} else {
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

func (s *UpdateSignalSubscriber) checkFeed(ctx context.Context) error {
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
		return err
	}
	entry, err := update.SelectProviderCandidate(raw, s.feed, update.ProviderFeedSelection{
		Exclude: func(entry update.ProviderFeedUpdate) bool {
			return update.HasFailedUpdate(s.deps.Paths, entry.UpdateID)
		},
		Channel: s.deps.Channel, Platform: runtime.GOOS, Arch: runtime.GOARCH, Kind: s.deps.Kind, CurrentSequence: state.Sequence,
	})
	if errors.Is(err, update.ErrNoCompatibleUpdate) {
		return nil
	}
	if err != nil {
		return err
	}
	if !s.claimFeed(entry.UpdateID) {
		return nil
	}
	succeeded := false
	defer func() { s.finishFeed(succeeded) }()
	// The index grants no authority. upgradeTriggered verifies the signed manifest and payload.
	signal := update.SignalFromFeedEntry(*entry, s.feed.String(), s.deps.Now(), false)
	if err := s.upgradeTriggered(ctx, signal, state, "feed-check"); err != nil {
		var refusal *update.StoreFormatRefusal
		if !errors.As(err, &refusal) {
			s.release(entry.UpdateID)
		}
		return err
	}
	succeeded = true
	return nil
}

// claimFeed reserves the install without charging the swap interval for a download.
func (s *UpdateSignalSubscriber) claimFeed(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pendingFeed || s.handled[id] || !s.lastUpgrade.IsZero() && s.deps.Now().Sub(s.lastUpgrade) < s.deps.MinInterval {
		return false
	}
	s.pendingFeed = true
	s.handled[id] = true
	return true
}

func (s *UpdateSignalSubscriber) finishFeed(succeeded bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pendingFeed = false
	if succeeded {
		s.lastUpgrade = s.deps.Now()
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

package node

import (
	"context"
	"errors"
	"math/rand"
	"os"
	"runtime"
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
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if err := s.checkFeed(ctx); err != nil {
				s.reportFailure("feed-check", "", err)
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
		Channel: s.deps.Channel, Platform: runtime.GOOS, Arch: runtime.GOARCH, Kind: s.deps.Kind, CurrentSequence: state.Sequence,
	})
	if err != nil {
		return err
	}
	if update.HasFailedUpdate(s.deps.Paths, entry.UpdateID) || !s.claim(entry.UpdateID) {
		return nil
	}
	// The index grants no authority. upgradeTriggered verifies the signed manifest and payload.
	signal := update.SignalFromFeedEntry(*entry, s.feed.String(), s.deps.Now(), false)
	if err := s.upgradeTriggered(ctx, signal, state, "feed-check"); err != nil {
		var refusal *update.StoreFormatRefusal
		if !errors.As(err, &refusal) {
			s.release(entry.UpdateID)
		}
		return err
	}
	return nil
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

func (s *UpdateSignalSubscriber) restoreFailedAlerts() {
	entries, err := os.ReadDir(s.deps.Paths.Failed)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Warnf("Read failed updates: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			s.deps.Alerts.Raise(ops.KindUpdateFailed, entry.Name(), ops.SeverityError, "Update failed its health gate and was rolled back; the release is quarantined")
		}
	}
}

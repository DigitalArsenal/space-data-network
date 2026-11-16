package storage

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
)

type delayedRebuildAPI struct {
	format4.API
	rebuilding atomic.Bool
	observed   chan struct{}
	release    chan struct{}
}

func (a *delayedRebuildAPI) Rebuild(ctx context.Context, typ string, what format4.RebuildWhat) ([]format4.RebuildRow, error) {
	a.rebuilding.Store(true)
	return []format4.RebuildRow{{Type: "CAT", Entries: 1}}, nil
}
func (a *delayedRebuildAPI) FTS(ctx context.Context) ([]format4.FTSState, error) {
	if !a.rebuilding.Load() {
		return a.API.FTS(ctx)
	}
	select {
	case <-a.observed:
	case a.observed <- struct{}{}:
	}
	select {
	case <-a.release:
		return []format4.FTSState{{Type: "CAT", State: "ready", Through: 1}}, nil
	default:
		return []format4.FTSState{{Type: "CAT", State: "building"}}, nil
	}
}

func TestFormat5RebuildIndexWaitsForFullTextReady(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "5")
	onFormat4Engine(t, func(t *testing.T) {
		open := format4OpenEngine
		delayed := &delayedRebuildAPI{observed: make(chan struct{}, 1), release: make(chan struct{})}
		format4OpenEngine = func(ctx context.Context, opts format4.Options) (format4.API, error) {
			api, err := open(ctx, opts)
			delayed.API = api
			return delayed, err
		}
		defer func() { format4OpenEngine = open }()
		store := openFormat4ForTest(t, t.TempDir())
		defer store.Close()
		done := make(chan error, 1)
		go func() { _, err := store.RebuildIndex(); done <- err }()
		select {
		case err := <-done:
			close(delayed.release)
			t.Fatalf("rebuild returned before observing full-text completion: %v", err)
		case <-delayed.observed:
		case <-time.After(time.Minute):
			close(delayed.release)
			t.Fatal("rebuild did not inspect SUMMARY")
		}
		select {
		case err := <-done:
			close(delayed.release)
			t.Fatalf("rebuild returned while building: %v", err)
		default:
		}
		close(delayed.release)
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Minute):
			t.Fatal("rebuild did not return after ready")
		}
	})
}

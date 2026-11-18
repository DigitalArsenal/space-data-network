package storage

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/marker"
)

// embeddingAPI decorates the engine by embedding format4.API, as the tests'
// wrappers do; it adds nothing.
type embeddingAPI struct{ format4.API }

// countingAPI decorates the engine and counts the preflight calls it forwards.
type countingAPI struct {
	format4.API
	validated *atomic.Int64
}

func (c countingAPI) ValidateType(spec format4.TypeSpec) error {
	c.validated.Add(1)
	return c.API.ValidateType(spec)
}

// ValidateType is part of format4.API: an unlimited store opened through a
// decorator of the engine API starts, and its registration preflight runs
// through the decorator. Before, the daemon asserted ValidateType as an
// optional interface, so an embedding decorator hid it and the unlimited
// store refused to start.
func TestFormat4UnlimitedStoreOpensThroughAnEngineDecorator(t *testing.T) {
	onFormat4Engine(t, func(t *testing.T) {
		open := format4OpenEngine
		t.Cleanup(func() { format4OpenEngine = open })
		var validated atomic.Int64
		for _, wrap := range []struct {
			name string
			fn   func(format4.API) format4.API
		}{
			{"embedding", func(api format4.API) format4.API { return embeddingAPI{api} }},
			{"counting", func(api format4.API) format4.API { return countingAPI{api, &validated} }},
		} {
			format4OpenEngine = func(ctx context.Context, opt format4.Options) (format4.API, error) {
				api, err := open(ctx, opt)
				if err != nil {
					return nil, err
				}
				return wrap.fn(api), nil
			}
			root := t.TempDir()
			s := openFormat4ForTest(t, root, WithUnlimitedSQLVisibility(true))
			unlimited := s.f4.opt.UnlimitedVisibility
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			m, err := marker.Read(root)
			if err != nil || !unlimited || m.Layout != 2 {
				t.Fatalf("%s decorator: unlimited=%v markers=%+v %v", wrap.name, unlimited, m, err)
			}
		}
		if validated.Load() == 0 {
			t.Fatal("the registration preflight did not run through the decorator")
		}
	})
}

package assetpin

import (
	"context"
	"errors"
	"sync"
)

// MutationGate serializes retention and API operations that can change the
// durable asset-pin ledger or its corresponding Kubo pins. Acquisition is
// context-aware so shutdown and request cancellation cannot wait forever.
type MutationGate struct {
	token chan struct{}
}

// NewMutationGate creates an unlocked asset-pin mutation gate.
func NewMutationGate() *MutationGate {
	gate := &MutationGate{token: make(chan struct{}, 1)}
	gate.token <- struct{}{}
	return gate
}

// Acquire waits for exclusive mutation ownership. The returned release
// function is idempotent.
func (g *MutationGate) Acquire(ctx context.Context) (func(), error) {
	if g == nil || g.token == nil {
		return nil, errors.New("asset pin mutation gate is required")
	}
	if ctx == nil {
		return nil, errors.New("asset pin mutation context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-g.token:
	}
	if err := ctx.Err(); err != nil {
		g.token <- struct{}{}
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() { g.token <- struct{}{} })
	}, nil
}

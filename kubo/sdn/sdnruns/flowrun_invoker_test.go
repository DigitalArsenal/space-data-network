package sdnruns

import (
	"context"
	"errors"
	"testing"

	"github.com/ipfs/kubo/sdn/modulert"
)

func TestModulertProviderInvokerValidates(t *testing.T) {
	if _, err := NewModulertProviderInvoker(nil, func(context.Context, string) ([]byte, error) { return nil, nil }, nil); err == nil {
		t.Fatal("expected error for nil loader")
	}
	if _, err := NewModulertProviderInvoker(func([]byte) (*modulert.Module, error) { return nil, nil }, nil, nil); err == nil {
		t.Fatal("expected error for nil resolver")
	}
}

func TestModulertProviderInvokerResolveError(t *testing.T) {
	inv, _ := NewModulertProviderInvoker(
		func([]byte) (*modulert.Module, error) { return nil, nil },
		func(context.Context, string) ([]byte, error) { return nil, errors.New("not installed") },
		nil,
	)
	if _, err := inv.InvokePull(context.Background(), "spacex"); err == nil {
		t.Fatal("expected resolve error to propagate")
	}
}

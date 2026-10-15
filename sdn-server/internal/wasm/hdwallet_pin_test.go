package wasm

// SEC-040 regressions: an hd-wallet module that is not a pinned release is
// refused before it is compiled, whether it comes as bytes or from a path.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// plantedHDWalletModule is the audit repro's module: the pinned build with an
// appended custom section. It is still valid wasm and still derives
// identities, but it is not the pinned release.
func plantedHDWalletModule(t *testing.T) []byte {
	t.Helper()
	embedded := EmbeddedHDWalletWasm()
	if embedded == nil {
		t.Fatal("embedded module does not match its pin")
	}
	name := []byte("sec040")
	body := append([]byte{byte(len(name))}, name...)
	body = append(body, []byte("attacker-controlled bytes")...)
	planted := append(append([]byte(nil), embedded...), 0x00, byte(len(body)))
	return append(planted, body...)
}

func TestCheckHDWalletPinAcceptsOnlyPinnedBuilds(t *testing.T) {
	t.Setenv(AllowUnpinnedHDWalletEnv, "")
	if err := CheckHDWalletPin(EmbeddedHDWalletWasm()); err != nil {
		t.Fatalf("the pinned embedded build was refused: %v", err)
	}
	if err := CheckHDWalletPin(plantedHDWalletModule(t)); !errors.Is(err, ErrHDWalletUnpinned) {
		t.Fatalf("planted module: err = %v, want ErrHDWalletUnpinned", err)
	}
	t.Setenv(AllowUnpinnedHDWalletEnv, "true")
	if err := CheckHDWalletPin(plantedHDWalletModule(t)); !errors.Is(err, ErrHDWalletUnpinned) {
		t.Fatal("the development escape hatch opened for a value other than exactly 1")
	}
	t.Setenv(AllowUnpinnedHDWalletEnv, "1")
	if err := CheckHDWalletPin(plantedHDWalletModule(t)); err != nil {
		t.Fatalf("the explicit development escape hatch did not apply: %v", err)
	}
}

func TestNewHDWalletModuleRefusesUnpinnedModule(t *testing.T) {
	t.Setenv(AllowUnpinnedHDWalletEnv, "")
	planted := plantedHDWalletModule(t)
	if hw, err := NewHDWalletModuleFromBytes(context.Background(), planted); !errors.Is(err, ErrHDWalletUnpinned) {
		if hw != nil {
			_ = hw.Close(context.Background())
		}
		t.Fatalf("NewHDWalletModuleFromBytes(planted): err = %v, want ErrHDWalletUnpinned", err)
	}
	// The CLI path: a node_modules copy in the working directory.
	path := filepath.Join(t.TempDir(), "node_modules", "hd-wallet-wasm", "dist", "hd-wallet-wasi.wasm")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, planted, 0o644); err != nil {
		t.Fatal(err)
	}
	if hw, err := NewHDWalletModule(context.Background(), path); !errors.Is(err, ErrHDWalletUnpinned) {
		if hw != nil {
			_ = hw.Close(context.Background())
		}
		t.Fatalf("NewHDWalletModule(planted path): err = %v, want ErrHDWalletUnpinned", err)
	}
}

func TestNewEmbeddedHDWalletModuleLoads(t *testing.T) {
	hw, err := NewEmbeddedHDWalletModule(context.Background())
	if err != nil {
		t.Fatalf("NewEmbeddedHDWalletModule: %v", err)
	}
	_ = hw.Close(context.Background())
}

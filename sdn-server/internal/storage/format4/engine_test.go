package format4_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
)

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	return ctx
}

func TestSelected(t *testing.T) {
	for v, want := range map[string]bool{"": false, "1": false, "2": false, "4": true, " 4 ": true, "sqlite": true, "SQLite": true, "sqlite3": false} {
		t.Setenv(format4.FormatEnv, v)
		if format4.Selected() != want {
			t.Fatalf("SDN_STORE_FORMAT=%q: Selected %v", v, !want)
		}
	}
	if typ, err := format4.TypeOf("OMM.fbs"); err != nil || typ != "OMM" {
		t.Fatalf("TypeOf: %q %v", typ, err)
	}
}

func TestStatusErrors(t *testing.T) {
	err := &format4.StatusError{Op: "PUT", Status: format4.StatusBusy, Msg: "credit"}
	if !errors.Is(err, format4.ErrBusy) || errors.Is(err, format4.ErrNoType) {
		t.Fatal("errors.Is does not match by status")
	}
	if !strings.Contains(err.Error(), "PUT") || format4.RejectReason(format4.RejectCIDForm) == "" {
		t.Fatal("error text")
	}
}

// A format-4 Open refuses formats 2 and 3, and a fresh create beside an
// unmigrated format-1 store, before it touches a file (contract §2.4).
func TestOpenRefusesOtherFormats(t *testing.T) {
	mk := func(t *testing.T, files map[string]string, dirs ...string) string {
		root := t.TempDir()
		for _, d := range dirs {
			if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		for name, body := range files {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return root
	}
	for _, tc := range []struct {
		name string
		root func(t *testing.T) string
		mode format4.CreateMode
		want error
	}{
		{"format 2 activated", func(t *testing.T) string {
			return mk(t, map[string]string{"fsql2/MIGRATED": "m", "fsql2/STORE": "s"}, "control.flatsqldb")
		}, format4.CreateFresh, format4.ErrWrongFormat},
		{"format 3 migrated, control still a file", func(t *testing.T) string {
			return mk(t, map[string]string{"fsql2/MIGRATED": "m", "control.flatsqldb": "db"})
		}, format4.OpenExisting, format4.ErrWrongFormat},
		{"format-2 activation begun", func(t *testing.T) string { return mk(t, nil, "control.flatsqldb") },
			format4.CreateForMigration, format4.ErrWrongFormat},
		{"unmigrated format 1, fresh", func(t *testing.T) string { return mk(t, map[string]string{"control.flatsqldb": "db"}) },
			format4.CreateFresh, format4.ErrNotMigrated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := tc.root(t)
			_, err := format4.Open(ctxT(t), format4.Options{DataRoot: root, Create: tc.mode, Wasm: []byte("not reached")})
			if !errors.Is(err, tc.want) {
				t.Fatalf("Open: %v, want %v", err, tc.want)
			}
			if _, err := os.Stat(filepath.Join(root, "fsql4")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("a refused Open created fsql4/: %v", err)
			}
		})
	}
}

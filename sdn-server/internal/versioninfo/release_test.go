package versioninfo

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestVersionReportsTheReleaseTagWhenStamped(t *testing.T) {
	prev := ReleaseTag
	defer func() { ReleaseTag = prev }()

	ReleaseTag = ""
	if got := Version(); got != SuiteVersion {
		t.Fatalf("development build Version() = %q, want the suite version %q", got, SuiteVersion)
	}
	if IsRelease() {
		t.Fatal("development build reports IsRelease")
	}
	ReleaseTag = "v1.0.4-beta.18"
	if got := Version(); got != "1.0.4-beta.18" {
		t.Fatalf("release build Version() = %q, want 1.0.4-beta.18", got)
	}
	if !IsRelease() {
		t.Fatal("release build does not report IsRelease")
	}
}

// The stamp survives the linker. stampprobe links this package and nothing
// that reads the stamp, and it is built with the release's link flags
// (-s -w strip the symbol table and DWARF; -trimpath as the release builds):
// the stamp must still be found, with this build's MaxStoreFormat. If the
// linker dropped it, a stamped release would read as unstamped and the update
// guard would refuse it on any format-2 store.
func TestStoreFormatStampSurvivesReleaseLinking(t *testing.T) {
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(goBin); err != nil {
		if goBin, err = exec.LookPath("go"); err != nil {
			t.Skip("no go toolchain to build the probe with")
		}
	}
	out := filepath.Join(t.TempDir(), "stampprobe")
	cmd := exec.Command(goBin, "build", "-trimpath", "-ldflags=-s -w", "-o", out, "./testdata/stampprobe")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if msg, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build the probe: %v\n%s", err, msg)
	}
	format, found, err := ReadStoreFormatStampFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !found || format != MaxStoreFormat {
		t.Fatalf("probe binary stamp: format %d found %v, want %d", format, found, MaxStoreFormat)
	}
	// And this test binary, linked by go test, carries it too.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if format, found, err := ReadStoreFormatStampFile(self); err != nil || !found || format != MaxStoreFormat {
		t.Fatalf("test binary stamp: format %d found %v err %v, want %d", format, found, err, MaxStoreFormat)
	}
}

func TestReadStoreFormatStamp(t *testing.T) {
	pad := func(n int) []byte { return bytes.Repeat([]byte{0xA5}, n) }
	cat := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	for _, tc := range []struct {
		name   string
		data   []byte
		format int
		found  bool
	}{
		{"unstamped", cat([]byte("\x7fELF"), pad(4096)), 0, false},
		{"stamped", cat(pad(100), StoreFormatStamp(2), pad(100)), 2, true},
		{"at the very end", cat(pad(100), StoreFormatStamp(3)), 3, true},
		// A stamp cut by the scanner's 1 MiB read boundary.
		{"across a read boundary", cat(pad(1<<20-7), StoreFormatStamp(4), pad(64)), 4, true},
		{"lowest of several", cat(StoreFormatStamp(5), pad(10), StoreFormatStamp(2), pad(10), StoreFormatStamp(3)), 2, true},
		// A header whose trailer does not check out is not a stamp.
		{"bad complement", cat(pad(8), StoreFormatStamp(2)[:storeFormatStampHeaderLen], []byte{storeFormatStampVersion, 2, 2}, pad(8)), 0, false},
		{"unknown version", cat(pad(8), StoreFormatStamp(2)[:storeFormatStampHeaderLen], []byte{9, 2, ^byte(2)}, pad(8)), 0, false},
		{"truncated", cat(pad(8), StoreFormatStamp(2)[:storeFormatStampHeaderLen+1]), 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			format, found, err := ReadStoreFormatStamp(bytes.NewReader(tc.data))
			if err != nil || format != tc.format || found != tc.found {
				t.Fatalf("got format %d found %v err %v, want %d %v", format, found, err, tc.format, tc.found)
			}
		})
	}
}

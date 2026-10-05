package update

// BUILD IDENTITY: which build answered.
//
// The helper's health gate used to ask one question — does the daemon say
// status=ok? — and never which daemon said it. An old build that survived the
// swap, or one a supervisor respawned in the middle of it, answered "ok" for
// the build the gate was supposed to be checking, and the helper reported a
// successful update with the previous binary still serving (expert review
// PLAT-01, SD-5). So the gate now also asks WHICH build it is talking to:
//
//   - the daemon reports, on /api/v1/id, the version of the bundle it was
//     started from and the sha256 of its own executable (RunningIdentity);
//   - the helper derives the same two facts from the bundle it has just put on
//     disk (BundleIdentity), and passes only when they agree (Check).
//
// The version is the BUNDLE's version (its manifest.json), not the binary's
// release stamp: the fleet lane publishes "<prefix>.<commit>" versions over
// binaries that report the suite version, and validateIncomingBundle already
// holds the bundle manifest's version equal to the signed update manifest's.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/spacedatanetwork/sdn-server/internal/bundle"
	"github.com/spacedatanetwork/sdn-server/internal/versioninfo"
)

// Identity is the build a running daemon reports on /api/v1/id.
type Identity struct {
	// BundleVersion is the version in the manifest.json of the bundle the
	// process was started from; empty outside a bundle.
	BundleVersion string `json:"bundle_version,omitempty"`
	// BuildSHA256 is the sha256 of the process's own executable.
	BuildSHA256 string `json:"build_sha256,omitempty"`
}

// Reported says whether the daemon reported any identity at all. A build from
// before the identity fields existed reports none.
func (id Identity) Reported() bool {
	return strings.TrimSpace(id.BundleVersion) != "" || strings.TrimSpace(id.BuildSHA256) != ""
}

func (id Identity) String() string {
	return fmt.Sprintf("bundle_version=%s build_sha256=%s", orDash(id.BundleVersion), orDash(id.BuildSHA256))
}

// RunningIdentity is THIS process's identity, read once: the bundle version
// from the manifest.json of the bundle its executable runs from, and its
// executable's sha256 (versioninfo.BuildSHA256).
func RunningIdentity() Identity { return runningIdentity() }

var runningIdentity = sync.OnceValue(func() Identity {
	id := Identity{BuildSHA256: versioninfo.BuildSHA256()}
	if layout := bundle.ResolveCurrent(); layout.Root != "" {
		if version, _, err := readBundleVersionAndChannel(layout.Root); err == nil {
			id.BundleVersion = version
		}
	}
	return id
})

// ExpectedIdentity is what the daemon started from a bundle must report.
type ExpectedIdentity struct {
	// BundleVersion is the version in the bundle's manifest.json.
	BundleVersion string
	// BuildSHA256 holds the sha256 of every daemon executable the bundle
	// carries (daemonBinaryPaths: the release layout's runtime/sdn/ binary,
	// the lean layout's bin/ one); the running daemon is one of them.
	BuildSHA256 []string
}

func (e ExpectedIdentity) String() string {
	shas := "-"
	if len(e.BuildSHA256) > 0 {
		shas = strings.Join(e.BuildSHA256, ",")
	}
	return fmt.Sprintf("bundle_version=%s build_sha256=%s", orDash(e.BundleVersion), shas)
}

// BundleIdentity reads the identity a daemon started from the bundle at root
// must report: the bundle manifest's version and the sha256 of each daemon
// executable in it. The helper reads it from the bundle root right after a
// swap, so the gate compares the running daemon with exactly what is on disk.
func BundleIdentity(root string) (ExpectedIdentity, error) {
	version, _, err := readBundleVersionAndChannel(root)
	if err != nil {
		return ExpectedIdentity{}, err
	}
	expected := ExpectedIdentity{BundleVersion: version}
	for _, rel := range daemonBinaryPaths {
		sha, ok, err := executableSHA256(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return ExpectedIdentity{}, fmt.Errorf("hash %s: %w", rel, err)
		}
		if ok {
			expected.BuildSHA256 = append(expected.BuildSHA256, sha)
		}
	}
	return expected, nil
}

// ErrIdentityUnreported is Check's answer for a daemon that reports no
// identity when one is required.
var ErrIdentityUnreported = errors.New("the daemon reports no build identity (bundle_version, build_sha256)")

// Check reports whether got is the expected build. A daemon that reports no
// identity at all passes only when require is false: a build from before the
// identity report cannot say which build it is, and the caller decides
// whether that is acceptable (a revert onto such a build) or not (the build an
// update has just installed, which reports it).
func (e ExpectedIdentity) Check(got Identity, require bool) error {
	if !got.Reported() {
		if require {
			return ErrIdentityUnreported
		}
		return nil
	}
	if e.BundleVersion != "" && got.BundleVersion != e.BundleVersion {
		return fmt.Errorf("the daemon that answered is not the installed build: it reports %s, the bundle on disk is %s", got, e)
	}
	if len(e.BuildSHA256) > 0 {
		for _, sha := range e.BuildSHA256 {
			if strings.EqualFold(sha, got.BuildSHA256) {
				return nil
			}
		}
		return fmt.Errorf("the daemon that answered is not the installed build: it reports %s, the bundle on disk is %s", got, e)
	}
	return nil
}

// executableSHA256 hashes path when it is a regular file that starts like an
// executable (executableMagic); a launcher script is not the daemon.
func executableSHA256(path string) (string, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !info.Mode().IsRegular() {
		return "", false, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	head := make([]byte, 4)
	if _, err := io.ReadFull(f, head); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return "", false, nil
		}
		return "", false, err
	}
	if !executableMagic(head) {
		return "", false, nil
	}
	h := sha256.New()
	h.Write(head)
	if _, err := io.Copy(h, f); err != nil {
		return "", false, err
	}
	return hex.EncodeToString(h.Sum(nil)), true, nil
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

package versioninfo

import "strings"

// ReleaseTag is the release this binary was cut as (for example
// "v1.0.4-beta.18"), stamped by the release runner through
//
//	-ldflags "-X github.com/spacedatanetwork/sdn-server/internal/versioninfo.ReleaseTag=v1.0.4-beta.18"
//
// A development build leaves it empty and reports the suite version.
var ReleaseTag string

// Version is the one version string every surface reports (VER-01): the
// release tag without its "v" when the binary was cut as a release, else the
// suite version from suite.versions.json.
func Version() string {
	if tag := strings.TrimSpace(ReleaseTag); tag != "" {
		return strings.TrimPrefix(tag, "v")
	}
	return SuiteVersion
}

// IsRelease reports whether this binary carries a release tag.
func IsRelease() bool { return strings.TrimSpace(ReleaseTag) != "" }

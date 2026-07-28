package ipfs

import (
	"fmt"
	"runtime"

	"github.com/ipfs/kubo/core/commands/cmdutils"
)

// CurrentCommit is the current git commit, this is set as a ldflag in the Makefile.
var CurrentCommit string

// CurrentVersionNumber is the current application's version literal.
const CurrentVersionNumber = "0.40.0-dev"

const ApiVersion = "/kubo/" + CurrentVersionNumber + "/" //nolint

// RepoVersion is the version number that we are currently expecting to see.
const RepoVersion = 18

// SDNAgentName is the libp2p identify agent this build presents.
//
// This fork is a Space Data Network node that happens to be built on kubo, and
// it must SAY so. Peers decide membership of the SDN accounts board from the
// identify agent-version, not from protocol participation (owner rule
// 2026-07-28: "This table should ONLY show spacedatanetwork nodes") — an
// upstream "kubo/..." string made this node an unexplainable row on our own
// board and then, once the rule landed, dropped it off the board entirely.
//
// The kubo version is preserved as build metadata after the "+", so nothing
// diagnostic is lost: the string reads "spacedatanetwork/<ver>+kubo/<ver>/<commit>".
const SDNAgentName = "spacedatanetwork"

// SDNAgentVersion is the Space Data Network suite version this node reports.
// Keep in step with sdn-server's internal/versioninfo.SuiteVersion.
const SDNAgentVersion = "1.0.4"

// GetUserAgentVersion is the libp2p user agent this node presents.
//
// Note: This will end in `/` when no commit is available. This is expected.
func GetUserAgentVersion() string {
	userAgent := SDNAgentName + "/" + SDNAgentVersion +
		"+kubo/" + CurrentVersionNumber + "/" + CurrentCommit
	if userAgentSuffix != "" {
		if CurrentCommit != "" {
			userAgent += "/"
		}
		userAgent += userAgentSuffix
	}
	return cmdutils.CleanAndTrim(userAgent)
}

var userAgentSuffix string

func SetUserAgentSuffix(suffix string) {
	userAgentSuffix = cmdutils.CleanAndTrim(suffix)
}

type VersionInfo struct {
	Version string
	Commit  string
	Repo    string
	System  string
	Golang  string
}

func GetVersionInfo() *VersionInfo {
	return &VersionInfo{
		Version: CurrentVersionNumber,
		Commit:  CurrentCommit,
		Repo:    fmt.Sprint(RepoVersion),
		System:  runtime.GOARCH + "/" + runtime.GOOS, // TODO: Precise version here
		Golang:  runtime.Version(),
	}
}

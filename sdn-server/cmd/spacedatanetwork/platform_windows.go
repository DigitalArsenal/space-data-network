package main

import (
	"os"
)

// Windows file ownership is a SID needing a LookupAccountSid round trip, and
// this is only ever used to make a permission error friendlier. An empty name
// is already handled by the caller, which falls back to "the service user".
func fileOwnerName(os.FileInfo) string { return "" }

// killSelf is the shutdown watchdog's last resort. Windows has no signals;
// os.Exit is the equivalent — uncatchable, no deferred work, process gone.
func killSelf() {
	os.Exit(1)
}

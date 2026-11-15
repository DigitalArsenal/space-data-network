//go:build !unix

package config

// pathOwnerUID: no uid ownership here (Windows), so no process owner is ever
// known and the running-daemon tier accepts nothing from the process table.
func pathOwnerUID(string) (int, bool) { return 0, false }

// firstRegularUID is unused where no owner is known.
func firstRegularUID() int { return 0 }

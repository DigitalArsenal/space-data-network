//go:build !linux

package keys

import "testing"

// The DMI permission fixture applies only to Linux.
func useTestMachineFingerprint(t *testing.T) { t.Helper() }

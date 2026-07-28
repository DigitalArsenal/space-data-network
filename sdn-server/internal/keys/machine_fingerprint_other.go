//go:build !linux && !darwin

package keys

// platformHardwareAttributes has no extra sources on unsupported platforms;
// derivation falls back to the arch/os/ncpu baseline in hardwareFingerprint.
func platformHardwareAttributes() []hwAttr { return nil }

// platformStableIdentifiers has no hardware source on unsupported platforms;
// v3 derivation falls back to hostname + os + arch.
func platformStableIdentifiers() []hwAttr { return nil }

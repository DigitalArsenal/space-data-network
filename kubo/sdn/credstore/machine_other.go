//go:build !linux && !darwin

package credstore

// platformHardwareAttributes has no extra sources on unsupported platforms;
// derivation falls back to the arch/os/ncpu baseline in hardwareFingerprint.
func platformHardwareAttributes() []hwAttr { return nil }

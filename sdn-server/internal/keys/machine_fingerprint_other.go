//go:build !linux && !darwin

package keys

// platformHardwareAttributes has no extra sources on unsupported platforms;
// derivation falls back to the arch/os/ncpu baseline in hardwareFingerprint.
func platformHardwareAttributes() []hwAttr { return nil }

// platformStableIdentifiers has no hardware source on unsupported platforms;
// v3 derivation falls back to hostname + os + arch.
func platformStableIdentifiers() []hwAttr { return nil }

// platformStableIdentifierReadings: no platform source exists here, so the v4
// fingerprint records the platform UUID as explicitly absent — a deliberate,
// reproducible input rather than a silent omission.
func platformStableIdentifierReadings() ([]sourceReading, error) {
	return []sourceReading{{key: "platuuid", absent: true}}, nil
}

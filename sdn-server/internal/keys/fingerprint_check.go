package keys

// A node asks, before it has sealed anything, whether the at-rest key could be
// derived from this machine for the account it runs as.
//
// On most Linux hosts the answer for any account but root is no: the kernel
// creates /sys/class/dmi/id/product_uuid with mode 0400, and v4 refuses to
// derive a key that would depend on the privilege of the reading process (see
// machine_fingerprint_v4.go). Installers ask this question so they can give a
// FRESH node a key password file before its first start instead of letting it
// fail; the daemon's own error asks it to explain that failure.
//
// The check reads the sources only. It runs no KDF, builds no key, and never
// reports a source VALUE: the verdict, the account name, and source names and
// states are all it carries.

import (
	"fmt"
	"strings"
)

// FingerprintStatus is the verdict of CheckMachineFingerprint.
type FingerprintStatus string

const (
	// FingerprintOK — every source was read; the v4 key is derivable and
	// includes the platform identifier.
	FingerprintOK FingerprintStatus = "ok"
	// FingerprintAbsent — the v4 key is derivable; the platform identifier
	// does not exist on this machine (a DMI-less VM, some containers) and is
	// recorded as absent, a deliberate and reproducible input.
	FingerprintAbsent FingerprintStatus = "absent"
	// FingerprintRefused — v4 refuses to derive for this account: a source
	// exists but cannot be read, or the account or hostname is unknown.
	// Nothing can be sealed under the machine key by this process.
	FingerprintRefused FingerprintStatus = "refused"
)

// FingerprintCheck is the outcome of CheckMachineFingerprint.
type FingerprintCheck struct {
	Status FingerprintStatus
	// User is the account the check ran as, when it could be resolved.
	User string
	// Reason says why, in operator terms. It names sources and their states
	// (or the refusal), never a source value.
	Reason string
	// Why is the platform's explanation of a refusal, when there is one (on
	// Linux: the kernel's 0400 on product_uuid). Empty otherwise.
	Why string
	// Err is the refusal itself; nil unless Status is FingerprintRefused.
	Err error
}

// CheckMachineFingerprint reports whether this process could derive the v4
// at-rest key. It reads the same sources the derivation reads, through the
// same code, so its verdict and DeriveDefaultPassword's success always agree
// — but it runs no KDF and returns no key material.
func CheckMachineFingerprint() FingerprintCheck {
	_, inputs, err := machineFingerprintV4()
	return classifyFingerprint(inputs, err)
}

// classifyFingerprint turns a v4 fingerprint outcome into a verdict. Split
// from CheckMachineFingerprint so every verdict is testable without this
// machine's actual sources.
func classifyFingerprint(inputs DerivationInputs, err error) FingerprintCheck {
	if err != nil {
		user, _ := currentProcessUsername()
		return FingerprintCheck{
			Status: FingerprintRefused,
			User:   user,
			Reason: err.Error(),
			Why:    refusalExplanation(err),
			Err:    err,
		}
	}
	var absent []string
	for _, s := range inputs.States {
		if s.State == SourceStateAbsent {
			absent = append(absent, s.Key)
		}
	}
	if len(absent) > 0 {
		return FingerprintCheck{
			Status: FingerprintAbsent,
			User:   inputs.User,
			Reason: fmt.Sprintf("derivable for account %q; source(s) %s do not exist on this machine and are recorded as absent (sources=[%s])",
				inputs.User, strings.Join(absent, ","), describeStates(inputs.States)),
		}
	}
	return FingerprintCheck{
		Status: FingerprintOK,
		User:   inputs.User,
		Reason: fmt.Sprintf("derivable for account %q (sources=[%s])", inputs.User, describeStates(inputs.States)),
	}
}

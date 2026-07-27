package main

// Locks the custody gates on `sdn key` (graph task nst-key-material-cli).
//
// The command's whole job is to move key material, so the tests that matter are
// the ones proving it REFUSES to move it in the wrong direction: no plaintext
// seed without an explicit flag, no silent identity replacement, no master xpub.

import (
	"strings"
	"testing"
)

// TestSecretFormatsRequireTheExplicitFlag is the central gate. A plaintext
// BIP-39 phrase is the entire node; exporting one must be a deliberate act.
func TestSecretFormatsRequireTheExplicitFlag(t *testing.T) {
	t.Parallel()

	_, err := validateExportFormat("mnemonic", false)
	if err == nil {
		t.Fatal("mnemonic exported without --insecure-plaintext")
	}
	for _, want := range []string{"SECRET", "--insecure-plaintext", "backup"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal should mention %q so the operator knows the safe path; got: %v", want, err)
		}
	}

	if _, err := validateExportFormat("mnemonic", true); err != nil {
		t.Fatalf("mnemonic refused even with --insecure-plaintext: %v", err)
	}
}

// TestPublicAndEncryptedFormatsNeedNoFlag locks that the gate is narrow: it
// applies to SECRET material only, so routine use is not trained to pass a
// scary flag (which would make the flag meaningless when it matters).
func TestPublicAndEncryptedFormatsNeedNoFlag(t *testing.T) {
	t.Parallel()

	for _, format := range []string{"xpub", "peerid", "epm", "vcard", "backup"} {
		if _, err := validateExportFormat(format, false); err != nil {
			t.Fatalf("format %q required --insecure-plaintext but is not SECRET: %v", format, err)
		}
	}
}

// TestEveryFormatIsClassified locks that a format cannot be added without
// declaring its custody class. The gate reads the class, so an unclassified
// format would silently default to printable.
func TestEveryFormatIsClassified(t *testing.T) {
	t.Parallel()

	if len(keyFormatInfo) == 0 {
		t.Fatal("no formats registered")
	}
	for format, info := range keyFormatInfo {
		if strings.TrimSpace(string(format)) == "" {
			t.Fatal("a format has an empty name")
		}
		if strings.TrimSpace(info.Description) == "" {
			t.Fatalf("format %q has no description; the help text is the operator's only warning", format)
		}
		if !info.Exportable && !info.Importable {
			t.Fatalf("format %q is neither exportable nor importable", format)
		}
		switch info.Custody {
		case custodyPublic, custodyEncrypted, custodySecret:
		default:
			t.Fatalf("format %q has an unknown custody class %d", format, info.Custody)
		}
	}
	// The seed is the only SECRET, and it must be one.
	if keyFormatInfo[keyFormatMnemonic].Custody != custodySecret {
		t.Fatal("mnemonic is not classified SECRET")
	}
	if keyFormatInfo[keyFormatBackup].Custody != custodyEncrypted {
		t.Fatal("backup is not classified encrypted")
	}
	for _, public := range []keyFormat{keyFormatXPub, keyFormatPeerID, keyFormatEPM, keyFormatVCard} {
		if keyFormatInfo[public].Custody != custodyPublic {
			t.Fatalf("%q is not classified public", public)
		}
	}
}

// TestOnlyIdentityBearingFormatsAreImportable locks that import is restricted
// to material that actually IS the node identity. Importing an xpub or a vCard
// would be importing someone's PUBLIC record, which belongs to the peer/account
// surface (§8), not to identity replacement.
func TestOnlyIdentityBearingFormatsAreImportable(t *testing.T) {
	t.Parallel()

	for _, format := range []string{"mnemonic", "backup"} {
		if _, err := validateImportFormat(format); err != nil {
			t.Fatalf("format %q should be importable: %v", format, err)
		}
	}
	for _, format := range []string{"xpub", "peerid", "epm", "vcard"} {
		if _, err := validateImportFormat(format); err == nil {
			t.Fatalf("format %q was accepted for identity import; it is a PUBLIC record, not this node's identity", format)
		}
	}
}

// TestUnknownFormatsAreRefusedWithGuidance locks that a typo produces the list
// of real formats rather than a bare failure.
func TestUnknownFormatsAreRefusedWithGuidance(t *testing.T) {
	t.Parallel()

	_, err := validateExportFormat("mnemonics", false)
	if err == nil {
		t.Fatal("a misspelled format was accepted")
	}
	if !strings.Contains(err.Error(), "xpub") {
		t.Fatalf("refusal should list the valid formats; got: %v", err)
	}

	if _, err := validateExportFormat("", false); err == nil {
		t.Fatal("an empty format was accepted")
	}
	if _, err := validateImportFormat("nonsense"); err == nil {
		t.Fatal("an unknown import format was accepted")
	}
}

// TestFormatNamesAreCaseAndSpaceTolerant locks light input normalisation — the
// operator typing "Mnemonic " should still hit the SECRET gate rather than
// falling through to "unknown format".
func TestFormatNamesAreCaseAndSpaceTolerant(t *testing.T) {
	t.Parallel()

	if _, err := validateExportFormat("  XPUB  ", false); err != nil {
		t.Fatalf("uppercase/padded format was refused: %v", err)
	}
	err := validateExportFormatSecretProbe(t, " Mnemonic ")
	if err == nil {
		t.Fatal("a padded, capitalised SECRET format bypassed the gate")
	}
	if !strings.Contains(err.Error(), "--insecure-plaintext") {
		t.Fatalf("normalised SECRET format hit the wrong error path: %v", err)
	}
}

func validateExportFormatSecretProbe(t *testing.T, name string) error {
	t.Helper()
	_, err := validateExportFormat(name, false)
	return err
}

// TestExportableAndImportableListsAreNonEmpty guards the help text: the flag
// descriptions are built from these, and an empty list would ship a command
// advertising no formats.
func TestExportableAndImportableListsAreNonEmpty(t *testing.T) {
	t.Parallel()

	exportable := knownKeyFormats(func(f keyFormat) bool { return keyFormatInfo[f].Exportable })
	importable := knownKeyFormats(func(f keyFormat) bool { return keyFormatInfo[f].Importable })

	if len(exportable) < 5 {
		t.Fatalf("exportable formats = %v, want the full public+encrypted+secret set", exportable)
	}
	if len(importable) != 2 {
		t.Fatalf("importable formats = %v, want exactly mnemonic and backup", importable)
	}
	// Sorted, so help output is stable.
	for i := 1; i < len(exportable); i++ {
		if exportable[i-1] > exportable[i] {
			t.Fatalf("exportable list is not sorted: %v", exportable)
		}
	}
}

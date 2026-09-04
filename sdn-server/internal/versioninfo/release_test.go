package versioninfo

import "testing"

func TestVersionReportsTheReleaseTagWhenStamped(t *testing.T) {
	prev := ReleaseTag
	defer func() { ReleaseTag = prev }()

	ReleaseTag = ""
	if got := Version(); got != SuiteVersion {
		t.Fatalf("development build Version() = %q, want the suite version %q", got, SuiteVersion)
	}
	if IsRelease() {
		t.Fatal("development build reports IsRelease")
	}
	ReleaseTag = "v1.0.4-beta.18"
	if got := Version(); got != "1.0.4-beta.18" {
		t.Fatalf("release build Version() = %q, want 1.0.4-beta.18", got)
	}
	if !IsRelease() {
		t.Fatal("release build does not report IsRelease")
	}
}

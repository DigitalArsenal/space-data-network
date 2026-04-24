package tlsmgr

import (
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/config"
)

func TestConfigTLSMode_BackfillsManagedModeFromLegacyTLSEnabled(t *testing.T) {
	cfg := config.Default()
	cfg.Admin.TLSMode = ""
	cfg.Admin.TLSEnabled = true
	cfg.Admin.TLSCertFile = ""
	cfg.Admin.TLSKeyFile = ""

	if got := cfg.Admin.EffectiveTLSMode(); got != ModeManaged {
		t.Fatalf("EffectiveTLSMode() = %q, want %q", got, ModeManaged)
	}
}

func TestConfigTLSMode_DefaultsToDisabledWhenLegacyTLSDisabled(t *testing.T) {
	cfg := config.Default()
	cfg.Admin.TLSEnabled = false
	cfg.Admin.TLSCertFile = ""
	cfg.Admin.TLSKeyFile = ""

	if got := cfg.Admin.EffectiveTLSMode(); got != ModeDisabled {
		t.Fatalf("EffectiveTLSMode() = %q, want %q", got, ModeDisabled)
	}
}

func TestConfigTLSMode_BackfillsStaticModeWhenLegacyFilesPresent(t *testing.T) {
	cfg := config.Default()
	cfg.Admin.TLSMode = ""
	cfg.Admin.TLSEnabled = true
	cfg.Admin.TLSCertFile = "/tmp/cert.pem"
	cfg.Admin.TLSKeyFile = "/tmp/key.pem"

	if got := cfg.Admin.EffectiveTLSMode(); got != ModeStatic {
		t.Fatalf("EffectiveTLSMode() = %q, want %q", got, ModeStatic)
	}
}

func TestNewRejectsUnsupportedTLSMode(t *testing.T) {
	_, err := New(config.AdminConfig{TLSMode: "wat"})
	if err == nil {
		t.Fatal("New() error = nil, want unsupported mode error")
	}
}

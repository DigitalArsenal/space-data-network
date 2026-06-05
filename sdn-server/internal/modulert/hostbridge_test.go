package modulert

import (
	"strings"
	"testing"
)

func TestHostcallImportModuleUsesSDKName(t *testing.T) {
	if HostcallImportModule != "space_data_module_host" {
		t.Fatalf("HostcallImportModule = %q, want space_data_module_host", HostcallImportModule)
	}
	retiredName := strings.Join([]string{"sdn", "host"}, "_")
	if HostcallImportModule == retiredName {
		t.Fatalf("HostcallImportModule still uses retired import module %q", retiredName)
	}
	if LegacyHostcallImportModule != retiredName {
		t.Fatalf("LegacyHostcallImportModule = %q, want %q", LegacyHostcallImportModule, retiredName)
	}
	if LegacyHostcallImportModule == HostcallImportModule {
		t.Fatalf("legacy import module should not replace canonical SDK module %q", HostcallImportModule)
	}
}

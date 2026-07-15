package modulert

import "testing"

func TestHostcallImportModuleUsesSDKName(t *testing.T) {
	if HostcallImportModule != "space_data_module_host" {
		t.Fatalf("HostcallImportModule = %q, want space_data_module_host", HostcallImportModule)
	}
}

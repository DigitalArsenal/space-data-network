package peers

import (
	"strings"
	"testing"
)

func TestAdminTemplateIncludesPluginModulesView(t *testing.T) {
	t.Parallel()

	required := []string{
		`data-tab="plugin-modules"`,
		`id="pluginModulesTable"`,
		`/api/v1/plugin-modules`,
		`function renderPluginModules`,
	}
	for _, want := range required {
		if !strings.Contains(adminTemplate, want) {
			t.Fatalf("admin template missing %q", want)
		}
	}
}

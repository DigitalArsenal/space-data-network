// Package license carries the minimal module-publish signing surface that the
// SDN module runtime (sdn/modulert) depends on.
//
// TRIMMED PORT (kubo rebase Phase 2b): only the pieces sdn/modulert needs are
// ported here — DefaultPluginRoot (used by capability_policy.go) and the
// ModulePublish* signing protocol (publish_protocol.go, used by module-publish
// signing + its test). The full license/entitlement store from
// sdn-server/internal/license was intentionally left behind.
package license

import "path/filepath"

// defaultPluginRootDirName mirrors the constant of the same name in
// sdn-server/internal/license/plugins.go.
const defaultPluginRootDirName = "plugins"

// DefaultPluginRoot returns the on-disk root under which module/plugin state
// is stored for a node rooted at baseDataPath.
func DefaultPluginRoot(baseDataPath string) string {
	return filepath.Join(baseDataPath, "license", defaultPluginRootDirName)
}

// PluginDependencyRef is a module bundle's declared dependency on another
// plugin/module by ID and version range. Ported from
// sdn-server/internal/license/plugins.go because ModulePublishEntry embeds it.
type PluginDependencyRef struct {
	PluginID   string `json:"plugin_id"`
	MinVersion string `json:"min_version,omitempty"`
	MaxVersion string `json:"max_version,omitempty"`
}

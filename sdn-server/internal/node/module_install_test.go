package node

import (
	"errors"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/deps"
	"github.com/spacedatanetwork/sdn-server/internal/license"
)

func indexOfStr(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

func TestCatalogInstallOrderDepsFirst(t *testing.T) {
	reg := writeTestPluginRegistry(t,
		license.PluginCatalogEntry{
			ID: "com.orbpro.a", Version: "1.0.0", EncryptedPath: "a.enc", KeyPath: "a.key",
			Dependencies: []license.PluginDependencyRef{{PluginID: "com.orbpro.b", MinVersion: "1.0.0"}},
		},
		license.PluginCatalogEntry{ID: "com.orbpro.b", Version: "1.0.0", EncryptedPath: "b.enc", KeyPath: "b.key"},
	)

	order, err := catalogInstallOrder(reg)
	if err != nil {
		t.Fatalf("catalogInstallOrder() error = %v", err)
	}
	ids := make([]string, 0, len(order))
	for _, step := range order {
		ids = append(ids, step.PluginID)
	}
	ib, ia := indexOfStr(ids, "com.orbpro.b"), indexOfStr(ids, "com.orbpro.a")
	if ib < 0 || ia < 0 {
		t.Fatalf("order = %v, want both plugins present", ids)
	}
	if ib > ia {
		t.Errorf("order = %v, want dependency com.orbpro.b before com.orbpro.a", ids)
	}
}

func TestCatalogInstallOrderTransitive(t *testing.T) {
	// a -> b -> c ; expect c, b, a.
	reg := writeTestPluginRegistry(t,
		license.PluginCatalogEntry{
			ID: "com.orbpro.a", Version: "1.0.0", EncryptedPath: "a.enc", KeyPath: "a.key",
			Dependencies: []license.PluginDependencyRef{{PluginID: "com.orbpro.b", MinVersion: "1.0.0"}},
		},
		license.PluginCatalogEntry{
			ID: "com.orbpro.b", Version: "1.0.0", EncryptedPath: "b.enc", KeyPath: "b.key",
			Dependencies: []license.PluginDependencyRef{{PluginID: "com.orbpro.c", MinVersion: "1.0.0"}},
		},
		license.PluginCatalogEntry{ID: "com.orbpro.c", Version: "1.0.0", EncryptedPath: "c.enc", KeyPath: "c.key"},
	)

	ids := catalogRegistrationOrder(reg)
	ic, ib, ia := indexOfStr(ids, "com.orbpro.c"), indexOfStr(ids, "com.orbpro.b"), indexOfStr(ids, "com.orbpro.a")
	if !(ic < ib && ib < ia) {
		t.Errorf("order = %v, want c before b before a", ids)
	}
}

func TestRegistryCatalogResolveMissing(t *testing.T) {
	reg := writeTestPluginRegistry(t)
	_, err := (registryCatalog{reg: reg}).Resolve(deps.Dependency{PluginID: "ghost"})
	if !errors.Is(err, deps.ErrDependencyNotFound) {
		t.Errorf("error = %v, want ErrDependencyNotFound", err)
	}
}

func TestCatalogRegistrationOrderFallbackOnMissingDep(t *testing.T) {
	// a depends on a plugin absent from the local catalog: ordering fails, but
	// registration falls back to listing order rather than dropping the plugin.
	reg := writeTestPluginRegistry(t,
		license.PluginCatalogEntry{
			ID: "com.orbpro.a", Version: "1.0.0", EncryptedPath: "a.enc", KeyPath: "a.key",
			Dependencies: []license.PluginDependencyRef{{PluginID: "com.orbpro.remote", MinVersion: "1.0.0"}},
		},
	)

	if _, err := catalogInstallOrder(reg); !errors.Is(err, deps.ErrDependencyNotFound) {
		t.Errorf("catalogInstallOrder() error = %v, want ErrDependencyNotFound", err)
	}
	ids := catalogRegistrationOrder(reg)
	if len(ids) != 1 || ids[0] != "com.orbpro.a" {
		t.Errorf("fallback registration order = %v, want [com.orbpro.a]", ids)
	}
}

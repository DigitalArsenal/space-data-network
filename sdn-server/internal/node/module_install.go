package node

import (
	"fmt"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/deps"
	"github.com/spacedatanetwork/sdn-server/internal/license"
)

// catalogRootID is a synthetic root that depends on every catalog plugin, so
// resolving its closure yields all catalog plugins in dependency-first order.
const catalogRootID = "__sdn_catalog_root__"

// registryCatalog adapts a license.PluginRegistry to deps.Catalog: it resolves a
// dependency to the local catalog asset's manifest, carrying that asset's
// declared PLG.DEPENDENCIES. This is the local "index" the resolver walks; a
// networked storefront index backs the same interface for remote pulls.
type registryCatalog struct {
	reg *license.PluginRegistry
}

// Resolve implements deps.Catalog against the local plugin registry.
func (c registryCatalog) Resolve(dep deps.Dependency) (deps.Manifest, error) {
	asset, ok := c.reg.Get(strings.TrimSpace(dep.PluginID))
	if !ok || asset == nil {
		return deps.Manifest{}, fmt.Errorf("%w: %s", deps.ErrDependencyNotFound, dep.PluginID)
	}
	return assetManifest(asset), nil
}

// assetManifest projects a plugin asset into the resolver's minimal manifest.
func assetManifest(a *license.PluginAsset) deps.Manifest {
	m := deps.Manifest{PluginID: a.ID, Version: a.Version}
	for _, d := range a.Dependencies {
		m.Dependencies = append(m.Dependencies, deps.Dependency{
			PluginID:   d.PluginID,
			MinVersion: d.MinVersion,
			MaxVersion: d.MaxVersion,
		})
	}
	return m
}

// catalogInstallOrder returns the registry's catalog plugins in dependency-first
// order (each plugin after everything it declares a dependency on), so runtime
// registration brings a dependency up before any module that composes with it.
// It errors (via ResolveClosure) if a declared dependency is absent from the
// local catalog or forms a cycle.
func catalogInstallOrder(reg *license.PluginRegistry) ([]deps.PlanStep, error) {
	if reg == nil {
		return nil, nil
	}
	catalog := registryCatalog{reg: reg}
	var rootDeps []deps.Dependency
	for _, descriptor := range reg.ListPublic() {
		id := strings.TrimSpace(descriptor.ID)
		if id == "" {
			continue
		}
		rootDeps = append(rootDeps, deps.Dependency{PluginID: id})
	}
	root := deps.Manifest{PluginID: catalogRootID, Dependencies: rootDeps}
	return deps.ResolveClosure(root, nil, catalog)
}

// catalogRegistrationOrder returns catalog plugin IDs in dependency-first order,
// falling back to the registry's listing order if the dependency graph cannot be
// resolved (e.g. a dependency lives on a remote provider not yet pulled).
func catalogRegistrationOrder(reg *license.PluginRegistry) []string {
	order, err := catalogInstallOrder(reg)
	if err != nil {
		log.Warnf("Catalog dependency ordering unavailable (%v); registering in listing order", err)
		var ids []string
		for _, descriptor := range reg.ListPublic() {
			ids = append(ids, descriptor.ID)
		}
		return ids
	}
	ids := make([]string, 0, len(order))
	for _, step := range order {
		ids = append(ids, step.PluginID)
	}
	return ids
}

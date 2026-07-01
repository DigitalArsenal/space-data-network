// Package deps implements the SDN module dependency resolver — the core of the
// "decentralized package manager" behavior. Given a root module's declared
// dependencies (PLG.DEPENDENCIES), the set of already-installed modules, and a
// catalog that can resolve a dependency requirement to a concrete published
// manifest, ResolveClosure returns the transitive set of modules that must be
// pulled and registered, ordered dependency-first (topological) so each module
// is installed only after everything it depends on.
//
// The package is deliberately transport- and registry-agnostic: the Catalog and
// InstalledSet interfaces let the same resolver drive the Go/Kubo node's local
// registry today and a networked storefront index (pulled through the grant
// flow) later, and the browser/Helia resolver mirrors this shape in JS.
package deps

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"golang.org/x/mod/semver"
)

// Sentinel errors returned (wrapped) by the resolver and catalog. Callers match
// them with errors.Is.
var (
	// ErrDependencyCycle means the dependency graph is not a DAG.
	ErrDependencyCycle = errors.New("dependency cycle")
	// ErrDependencyNotFound means no version of a required plugin is published.
	ErrDependencyNotFound = errors.New("dependency not found")
	// ErrNoSatisfyingVersion means versions exist but none fall in the range.
	ErrNoSatisfyingVersion = errors.New("no satisfying version")
	// ErrVersionConflict means an installed or already-chosen version cannot
	// satisfy a later, incompatible requirement for the same plugin.
	ErrVersionConflict = errors.New("version conflict")
	// ErrInvalidVersion means a version or range bound is not valid semver.
	ErrInvalidVersion = errors.New("invalid version")
)

// Dependency is a single dependency requirement: a plugin ID with an optional
// inclusive semver version range. It mirrors PLG.PluginDependency and
// license.PluginDependencyRef. Empty bounds are unbounded on that side.
type Dependency struct {
	PluginID   string
	MinVersion string
	MaxVersion string
}

// Manifest is the minimal view of a plugin the resolver needs: its identity and
// its direct dependencies.
type Manifest struct {
	PluginID     string
	Version      string
	Dependencies []Dependency
}

// Catalog resolves a dependency requirement to the concrete manifest of the best
// (highest) published version that satisfies the range. It is the resolver's
// index — backed by the local plugin registry today and a networked storefront
// index later. Implementations should return an error wrapping
// ErrDependencyNotFound when no version is published, or ErrNoSatisfyingVersion
// when versions exist but none fall in the requested range.
type Catalog interface {
	Resolve(dep Dependency) (Manifest, error)
}

// InstalledSet reports the version of an already-installed plugin.
type InstalledSet interface {
	InstalledVersion(pluginID string) (version string, ok bool)
}

// PlanStep is one module to install. ResolveClosure returns steps
// dependency-first: every step's dependencies appear earlier in the slice.
type PlanStep struct {
	PluginID string
	Version  string
}

// ResolveClosure walks root's transitive dependency graph and returns the
// modules that must be installed, ordered so every dependency precedes its
// dependents. Modules already installed at a satisfying version are skipped and
// not re-walked (they are trusted to be complete). The root itself is never
// included — the caller installs root and this closure alongside it.
//
// It returns an error wrapping one of the sentinels on a cycle, a missing or
// unsatisfiable dependency, an incompatible diamond (two dependents pin
// conflicting ranges), or an invalid version/range.
func ResolveClosure(root Manifest, installed InstalledSet, catalog Catalog) ([]PlanStep, error) {
	if catalog == nil {
		return nil, errors.New("deps: catalog is required")
	}
	r := &resolution{
		installed: installed,
		catalog:   catalog,
		chosen:    make(map[string]string),
		inStack:   make(map[string]bool),
	}
	if rootID := strings.TrimSpace(root.PluginID); rootID != "" {
		// Mark root in-stack so a transitive dependency back onto root is
		// detected as a cycle rather than an infinite descent.
		r.inStack[rootID] = true
	}
	if err := r.visitDeps(root); err != nil {
		return nil, err
	}
	return r.plan, nil
}

type resolution struct {
	installed InstalledSet
	catalog   Catalog
	chosen    map[string]string // pluginID -> version selected into the plan
	inStack   map[string]bool   // pluginIDs on the current DFS path (cycle guard)
	plan      []PlanStep
}

// visitDeps resolves each direct dependency of m in post-order: a dependency is
// appended to the plan only after its own dependencies have been.
func (r *resolution) visitDeps(m Manifest) error {
	deps := append([]Dependency(nil), m.Dependencies...)
	// Deterministic traversal for stable, reproducible plans.
	sort.Slice(deps, func(i, j int) bool { return deps[i].PluginID < deps[j].PluginID })

	for _, dep := range deps {
		id := strings.TrimSpace(dep.PluginID)
		if id == "" {
			continue
		}
		if err := validateRange(dep); err != nil {
			return fmt.Errorf("dependency %s: %w", id, err)
		}

		// Already installed: trust it if it satisfies, else hard conflict.
		if v, ok := r.installedVersion(id); ok {
			if satisfies(v, dep) {
				continue
			}
			return fmt.Errorf("%w: %s installed %s does not satisfy [%s,%s]",
				ErrVersionConflict, id, v, orAny(dep.MinVersion), orAny(dep.MaxVersion))
		}

		// Already chosen earlier in this closure (diamond): reuse if the chosen
		// version also satisfies this requirement, else conflict.
		if v, ok := r.chosen[id]; ok {
			if satisfies(v, dep) {
				continue
			}
			return fmt.Errorf("%w: %s chosen %s does not satisfy [%s,%s]",
				ErrVersionConflict, id, v, orAny(dep.MinVersion), orAny(dep.MaxVersion))
		}

		if r.inStack[id] {
			return fmt.Errorf("%w: at %s", ErrDependencyCycle, id)
		}

		depManifest, err := r.catalog.Resolve(dep)
		if err != nil {
			return fmt.Errorf("resolve %s: %w", id, err)
		}
		if strings.TrimSpace(depManifest.PluginID) == "" {
			depManifest.PluginID = id
		}
		if !satisfies(depManifest.Version, dep) {
			return fmt.Errorf("%w: %s resolved to %q outside [%s,%s]",
				ErrNoSatisfyingVersion, id, depManifest.Version, orAny(dep.MinVersion), orAny(dep.MaxVersion))
		}

		r.inStack[id] = true
		if err := r.visitDeps(depManifest); err != nil {
			return err
		}
		delete(r.inStack, id)

		r.chosen[id] = depManifest.Version
		r.plan = append(r.plan, PlanStep{PluginID: id, Version: depManifest.Version})
	}
	return nil
}

func (r *resolution) installedVersion(id string) (string, bool) {
	if r.installed == nil {
		return "", false
	}
	return r.installed.InstalledVersion(id)
}

// validateRange checks that provided bounds are valid semver and ordered.
func validateRange(dep Dependency) error {
	if dep.MinVersion != "" && normalizeSemver(dep.MinVersion) == "" {
		return fmt.Errorf("%w: min %q", ErrInvalidVersion, dep.MinVersion)
	}
	if dep.MaxVersion != "" && normalizeSemver(dep.MaxVersion) == "" {
		return fmt.Errorf("%w: max %q", ErrInvalidVersion, dep.MaxVersion)
	}
	if dep.MinVersion != "" && dep.MaxVersion != "" &&
		semver.Compare(normalizeSemver(dep.MinVersion), normalizeSemver(dep.MaxVersion)) > 0 {
		return fmt.Errorf("%w: min %s > max %s", ErrInvalidVersion, dep.MinVersion, dep.MaxVersion)
	}
	return nil
}

// normalizeSemver returns a comparable "v"-prefixed semver string, or "" if v is
// not valid semver.
func normalizeSemver(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if !semver.IsValid(v) {
		return ""
	}
	return v
}

// satisfies reports whether version falls within dep's inclusive [min,max]
// range. Empty bounds are unbounded. A version that is not valid semver fails
// closed.
func satisfies(version string, dep Dependency) bool {
	cv := normalizeSemver(version)
	if cv == "" {
		return false
	}
	if dep.MinVersion != "" {
		min := normalizeSemver(dep.MinVersion)
		if min == "" || semver.Compare(cv, min) < 0 {
			return false
		}
	}
	if dep.MaxVersion != "" {
		max := normalizeSemver(dep.MaxVersion)
		if max == "" || semver.Compare(cv, max) > 0 {
			return false
		}
	}
	return true
}

func orAny(v string) string {
	if strings.TrimSpace(v) == "" {
		return "*"
	}
	return v
}

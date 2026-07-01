package deps

import (
	"fmt"
	"strings"

	"golang.org/x/mod/semver"
)

// MapCatalog is an in-memory Catalog backed by published manifests keyed by
// plugin ID. For a dependency it selects the highest published version that
// satisfies the requested range. It backs unit tests and is a ready building
// block for a local, registry-fed catalog.
type MapCatalog struct {
	byID map[string][]Manifest
}

// NewMapCatalog builds a MapCatalog from the given published manifests.
func NewMapCatalog(manifests ...Manifest) *MapCatalog {
	c := &MapCatalog{byID: make(map[string][]Manifest)}
	for _, m := range manifests {
		c.Add(m)
	}
	return c
}

// Add registers a published manifest. Multiple versions of the same plugin ID
// may be added; Resolve picks the best match per request.
func (c *MapCatalog) Add(m Manifest) {
	id := strings.TrimSpace(m.PluginID)
	if id == "" {
		return
	}
	if c.byID == nil {
		c.byID = make(map[string][]Manifest)
	}
	c.byID[id] = append(c.byID[id], m)
}

// Resolve returns the highest published version of dep.PluginID that satisfies
// the requested range.
func (c *MapCatalog) Resolve(dep Dependency) (Manifest, error) {
	id := strings.TrimSpace(dep.PluginID)
	candidates := c.byID[id]
	if len(candidates) == 0 {
		return Manifest{}, fmt.Errorf("%w: %s", ErrDependencyNotFound, id)
	}
	var best Manifest
	var bestCanonical string
	found := false
	for _, m := range candidates {
		if !satisfies(m.Version, dep) {
			continue
		}
		cv := normalizeSemver(m.Version)
		if !found || semver.Compare(cv, bestCanonical) > 0 {
			best, bestCanonical, found = m, cv, true
		}
	}
	if !found {
		return Manifest{}, fmt.Errorf("%w: %s [%s,%s]",
			ErrNoSatisfyingVersion, id, orAny(dep.MinVersion), orAny(dep.MaxVersion))
	}
	return best, nil
}

// MapInstalled is an in-memory InstalledSet mapping plugin ID to installed
// version.
type MapInstalled map[string]string

// InstalledVersion implements InstalledSet.
func (m MapInstalled) InstalledVersion(pluginID string) (string, bool) {
	v, ok := m[strings.TrimSpace(pluginID)]
	return v, ok
}

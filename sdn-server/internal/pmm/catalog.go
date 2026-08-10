package pmm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CatalogFile is the DATA that declares what this node offers. It is deployed
// config, not code: tiers, access policies and browse visibility are owner
// rulings and arrive here, never as a table compiled into the daemon.
type CatalogFile struct {
	ProviderName string  `json:"provider_name"`
	Description  string  `json:"description"`
	Entries      []Entry `json:"entries"`

	// Browse carries the synthesized, non-IDL fields the storefront uses.
	// Kept OUT of the record and out of the signed statement.
	Browse []BrowseHint `json:"browse"`
}

// BrowseHint is synthesized UI metadata. Lowercase keys on purpose: only SDS
// record fields carry IDL capitalization.
type BrowseHint struct {
	ModuleID        string `json:"module_id"`
	Family          string `json:"family"`
	ScenarioVisible bool   `json:"scenario_visible"`
	Open            bool   `json:"open"`
	ModulePath      string `json:"module_path"`
}

// LoadCatalog reads the catalog file and re-hashes every artifact from disk.
//
// The hash is ALWAYS recomputed from the bytes on disk rather than trusted from
// the file. A catalog that disagrees with the artifact it names is the exact
// failure this record exists to prevent, and a stale declared hash would be
// signed into a manifest clients then reject.
func LoadCatalog(path, artifactRoot string) (*CatalogFile, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("pmm: read catalog %s: %w", path, err)
	}
	var cf CatalogFile
	if err := json.Unmarshal(raw, &cf); err != nil {
		return nil, fmt.Errorf("pmm: decode catalog %s: %w", path, err)
	}
	for i := range cf.Entries {
		e := &cf.Entries[i]
		if strings.TrimSpace(e.SourceArtifact) == "" {
			continue // metadata-only entry (ENTITLED); nothing served anonymously
		}
		full := filepath.Join(artifactRoot, e.SourceArtifact)
		hash, size, err := HashArtifact(full)
		if err != nil {
			return nil, err
		}
		if e.ContentHash != "" && e.ContentHash != hash {
			return nil, fmt.Errorf(
				"pmm: %s CONTENT_HASH mismatch: catalog says %s, artifact %s hashes to %s",
				e.ModuleID, e.ContentHash, full, hash)
		}
		e.ContentHash = hash
		e.SizeBytes = size
	}
	return &cf, nil
}

// BuildManifest assembles and signs the record from catalog data plus the node's
// own identity. ttl bounds anonymous trust: an unexpiring signed manifest cannot
// be withdrawn, so a zero or negative ttl is refused.
func BuildManifest(cf *CatalogFile, trust TrustAnchor, epoch uint64, canonicalURL string, now time.Time, ttl time.Duration, signer Signer) (*Manifest, error) {
	if ttl <= 0 {
		return nil, fmt.Errorf("pmm: EXPIRES_AT requires a positive ttl, got %s", ttl)
	}
	stamp := func(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }
	m := &Manifest{
		ProviderDomain: trust.ProviderDomain,
		ProviderName:   cf.ProviderName,
		Description:    cf.Description,
		Epoch:          epoch,
		Trust:          trust,
		Modules:        append([]Entry(nil), cf.Entries...),
		CanonicalURL:   canonicalURL,
		CreatedAt:      stamp(now),
		UpdatedAt:      stamp(now),
		ExpiresAt:      stamp(now.Add(ttl)),
	}
	for i := range m.Modules {
		if m.Modules[i].UpdatedAt == "" {
			m.Modules[i].UpdatedAt = stamp(now)
		}
		if m.Modules[i].Epoch == 0 {
			m.Modules[i].Epoch = 1
		}
		// Catalog input, not record content. Dropped here so the provider's
		// local disk layout can never reach a client through any projection.
		m.Modules[i].SourceArtifact = ""
	}
	if err := Sign(m, signer); err != nil {
		return nil, err
	}
	return m, nil
}

// CatalogProviderDomain reads just the provider domain from a catalog file.
//
// The domain lives in the catalog rather than the daemon's config schema so this
// surface can be added without a config migration, and so the domain can never
// disagree with the catalog it describes.
func CatalogProviderDomain(path string) (string, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("pmm: read catalog %s: %w", path, err)
	}
	var head struct {
		ProviderDomain string `json:"provider_domain"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return "", fmt.Errorf("pmm: decode catalog %s: %w", path, err)
	}
	return strings.TrimSpace(head.ProviderDomain), nil
}

// CatalogPluginTypes reads PLUGIN_ID -> PLUGIN_TYPE and nothing else.
//
// PLUGIN_TYPE is the module's family and the ONLY sanctioned way to group an
// offering (see Entry.PluginType). Every surface that shelves modules by
// category therefore has to read it from HERE — the one declared, deployed
// truth — rather than carry a second copy that can disagree with this one.
//
// LoadCatalog re-hashes every artifact from disk, which is the right cost when
// a manifest is about to be signed and the wrong cost for a metadata read on a
// request path. This does the decode and stops.
//
// A blank PLUGIN_TYPE is DROPPED, never defaulted. The enum's zero value is
// Sensor, so a defaulted blank is not a missing answer, it is a wrong one.
func CatalogPluginTypes(path string) (map[string]string, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("pmm: read catalog %s: %w", path, err)
	}
	var cf struct {
		Entries []struct {
			ModuleID   string `json:"MODULE_ID"`
			PluginID   string `json:"PLUGIN_ID"`
			PluginType string `json:"PLUGIN_TYPE"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(raw, &cf); err != nil {
		return nil, fmt.Errorf("pmm: decode catalog %s: %w", path, err)
	}
	types := make(map[string]string, len(cf.Entries))
	for _, e := range cf.Entries {
		declared := strings.TrimSpace(e.PluginType)
		if declared == "" {
			continue
		}
		// Keyed by both identifiers: the licensing catalog keys assets by the
		// plugin ID, and a catalog that omits PLUGIN_ID is declaring that the
		// module ID is the plugin ID.
		for _, key := range []string{e.PluginID, e.ModuleID} {
			if k := strings.TrimSpace(key); k != "" {
				types[k] = declared
			}
		}
	}
	return types, nil
}

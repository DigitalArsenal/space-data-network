package api

// Origin registry for ingest connectors (fbcs program).
//
// Provenance is structural: the PRODUCER of a record is the signed $EPM of the
// node that ingested it, and the ORIGIN is the upstream organisation that
// published the bytes. The origin rides on the $ICN connector record and is
// resolved, in order, from
//
//	(a) the ledger row's origin columns (written from the ingest meta keys
//	    origin_id / origin_name / dataset_id the module declares),
//	(b) config connectors.origins[] (operator declaration),
//	(c) this compiled registry (the CelesTrak lanes the fleet runs),
//	(d) the host of the connector's endpoint URL,
//	(e) nothing — never a tag string.
//
// Licence terms are NOT compiled here: only the module that pulled a document
// knows which terms it carries, so licence fields arrive from the ingest meta
// (sdn_source_batch_license) or from config.

import (
	"net/url"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/sourcemetrics"
)

// ConnectorOrigin is the resolved origin of one connector lane.
type ConnectorOrigin struct {
	OriginID   string
	OriginName string
	DatasetID  string
	License    string
	LicenseURL string
	Citation   string
	// PollIntervalMs is the registry cadence for the lane, used when no
	// running flow declares a timer for it. 0 when unknown.
	PollIntervalMs int64
	// PrimarySchema is the lane's primary emitted schema in store form
	// ("OMM.fbs"), used for TARGET_SCHEMA when the lane emits several.
	PrimarySchema string
}

const (
	celestrakOriginID   = "celestrak.org"
	celestrakOriginName = "CelesTrak"

	hourMs = int64(3600 * 1000)
	dayMs  = 24 * hourMs
)

// celestrakConnectorOrigins is the compiled registry, keyed by source_name.
// Dataset ids and cadences follow the program's CelesTrak dataset registry.
var celestrakConnectorOrigins = map[string]ConnectorOrigin{
	"celestrak-gp": {
		OriginID: celestrakOriginID, OriginName: celestrakOriginName,
		DatasetID: "gp-full-catalog", PollIntervalMs: 3 * hourMs, PrimarySchema: "OMM.fbs",
	},
	"celestrak-satcat": {
		OriginID: celestrakOriginID, OriginName: celestrakOriginName,
		DatasetID: "satcat", PollIntervalMs: dayMs, PrimarySchema: "CAT.fbs",
	},
	"celestrak-satcat-csv": {
		OriginID: celestrakOriginID, OriginName: celestrakOriginName,
		DatasetID: "satcat-csv", PollIntervalMs: dayMs, PrimarySchema: "CAT.fbs",
	},
	"celestrak-space-weather": {
		OriginID: celestrakOriginID, OriginName: celestrakOriginName,
		DatasetID: "sw-all", PollIntervalMs: 3 * hourMs, PrimarySchema: "SPW.fbs",
	},
	"celestrak-eop": {
		OriginID: celestrakOriginID, OriginName: celestrakOriginName,
		DatasetID: "eop-all", PollIntervalMs: dayMs, PrimarySchema: "EOP.fbs",
	},
	"celestrak-socrates-minrange": {
		OriginID: celestrakOriginID, OriginName: celestrakOriginName,
		DatasetID: "socrates-minrange", PollIntervalMs: 8 * hourMs, PrimarySchema: "CSM.fbs",
	},
	"celestrak-socrates-maxprob": {
		OriginID: celestrakOriginID, OriginName: celestrakOriginName,
		DatasetID: "socrates-maxprob", PollIntervalMs: 8 * hourMs, PrimarySchema: "CSM.fbs",
	},
	"celestrak-gp-groups": {
		OriginID: celestrakOriginID, OriginName: celestrakOriginName,
		DatasetID: "gp-groups", PollIntervalMs: dayMs, PrimarySchema: "EGP.fbs",
	},
	"celestrak-satcat-launch-sites": {
		OriginID: celestrakOriginID, OriginName: celestrakOriginName,
		DatasetID: "satcat-launch-sites", PollIntervalMs: 7 * dayMs, PrimarySchema: "SIT.fbs",
	},
	"celestrak-satcat-owners": {
		OriginID: celestrakOriginID, OriginName: celestrakOriginName,
		DatasetID: "satcat-owners", PollIntervalMs: 7 * dayMs, PrimarySchema: "LCC.fbs",
	},
}

// RegistryConnectorOrigin returns the compiled registry entry for a source
// name, when the fleet knows the lane.
func RegistryConnectorOrigin(sourceName string) (ConnectorOrigin, bool) {
	origin, ok := celestrakConnectorOrigins[strings.TrimSpace(sourceName)]
	return origin, ok
}

// configConnectorOrigin finds the first config declaration matching the lane:
// an exact provider_id + source_name pair first, then a source_prefix.
func configConnectorOrigin(origins []config.ConnectorOriginConfig, providerID, sourceName string) (ConnectorOrigin, bool) {
	providerID = strings.TrimSpace(providerID)
	sourceName = strings.TrimSpace(sourceName)
	toOrigin := func(c config.ConnectorOriginConfig) ConnectorOrigin {
		return ConnectorOrigin{
			OriginID:   strings.TrimSpace(c.OriginID),
			OriginName: strings.TrimSpace(c.OriginName),
			DatasetID:  strings.TrimSpace(c.DatasetID),
			License:    strings.TrimSpace(c.License),
			LicenseURL: strings.TrimSpace(c.LicenseURL),
			Citation:   strings.TrimSpace(c.Citation),
		}
	}
	for _, c := range origins {
		cp, cs := strings.TrimSpace(c.ProviderID), strings.TrimSpace(c.SourceName)
		if cs == "" || cs != sourceName {
			continue
		}
		if cp != "" && cp != providerID {
			continue
		}
		if strings.TrimSpace(c.OriginID) == "" {
			continue
		}
		return toOrigin(c), true
	}
	for _, c := range origins {
		prefix := strings.TrimSpace(c.SourcePrefix)
		if prefix == "" || !strings.HasPrefix(sourceName, prefix) {
			continue
		}
		if cp := strings.TrimSpace(c.ProviderID); cp != "" && cp != providerID {
			continue
		}
		if strings.TrimSpace(c.OriginID) == "" {
			continue
		}
		return toOrigin(c), true
	}
	return ConnectorOrigin{}, false
}

// endpointHostOrigin derives an origin id from an http(s) endpoint host.
func endpointHostOrigin(endpointURL string) (ConnectorOrigin, bool) {
	u, err := url.Parse(strings.TrimSpace(endpointURL))
	if err != nil {
		return ConnectorOrigin{}, false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return ConnectorOrigin{}, false
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	if host == "" {
		return ConnectorOrigin{}, false
	}
	return ConnectorOrigin{OriginID: host}, true
}

// ResolveConnectorOrigin applies the contract order. The first tier that
// names an ORIGIN_ID supplies the identity; later tiers only fill fields the
// chosen tier left empty (dataset id, licence, citation, cadence, primary
// schema), and an origin NAME is taken from a later tier only when that tier
// names the same origin.
func ResolveConnectorOrigin(ledger *sourcemetrics.Source, configOrigins []config.ConnectorOriginConfig, providerID, sourceName, endpointURL string) ConnectorOrigin {
	tiers := make([]ConnectorOrigin, 0, 4)
	if ledger != nil && strings.TrimSpace(ledger.OriginID) != "" {
		tiers = append(tiers, ConnectorOrigin{
			OriginID:   strings.TrimSpace(ledger.OriginID),
			OriginName: strings.TrimSpace(ledger.OriginName),
			DatasetID:  strings.TrimSpace(ledger.DatasetID),
		})
	}
	if o, ok := configConnectorOrigin(configOrigins, providerID, sourceName); ok {
		tiers = append(tiers, o)
	}
	if o, ok := RegistryConnectorOrigin(sourceName); ok {
		tiers = append(tiers, o)
	}
	if o, ok := endpointHostOrigin(endpointURL); ok {
		tiers = append(tiers, o)
	}
	if len(tiers) == 0 {
		return ConnectorOrigin{}
	}
	out := tiers[0]
	for _, tier := range tiers[1:] {
		if out.OriginName == "" && tier.OriginID == out.OriginID {
			out.OriginName = tier.OriginName
		}
		if out.DatasetID == "" {
			out.DatasetID = tier.DatasetID
		}
		if out.License == "" && out.LicenseURL == "" {
			out.License, out.LicenseURL = tier.License, tier.LicenseURL
		}
		if out.Citation == "" {
			out.Citation = tier.Citation
		}
		if out.PollIntervalMs == 0 {
			out.PollIntervalMs = tier.PollIntervalMs
		}
		if out.PrimarySchema == "" {
			out.PrimarySchema = tier.PrimarySchema
		}
	}
	return out
}

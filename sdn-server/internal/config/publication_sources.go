package config

import (
	"fmt"
	"os"
	"strings"
)

func (c *Config) validatePublicationSources() error {
	for i := range c.Publishing.AutoPublish {
		rule := &c.Publishing.AutoPublish[i]
		if rule.CatalogObjectLimit < 0 || rule.CatalogObjectLimit > 100000 {
			return fmt.Errorf("catalog_object_limit must be between 1 and 100000, or zero for the default")
		}
		if rule.RetainForPublication || rule.PairedPublication || rule.CatalogProductSource != "" || rule.StopPublicationRetention || len(rule.RequeuePublicationBatches) > 0 || len(rule.DiscardPublicationBatches) > 0 {
			rule.Schema = strings.ToUpper(strings.TrimSuffix(strings.ToLower(strings.TrimSpace(rule.Schema)), ".fbs")) + ".fbs"
		}
	}
	retained := map[string]bool{}
	key := func(schema, provider, source string) string {
		return strings.ToUpper(strings.TrimSuffix(strings.ToLower(strings.TrimSpace(schema)), ".fbs")) + "/" + strings.TrimSpace(provider) + "/" + strings.TrimSpace(source)
	}
	for _, rule := range c.Publishing.AutoPublish {
		if rule.RetainForPublication {
			retained[key(rule.Schema, rule.ProviderID, rule.SourceName)] = true
		}
	}
	for _, rule := range c.Publishing.AutoPublish {
		if len(rule.RequeuePublicationBatches) > 0 || len(rule.DiscardPublicationBatches) > 0 {
			if strings.TrimSpace(rule.ProviderID) == "" || strings.TrimSpace(rule.SourceName) == "" || strings.ContainsAny(rule.ProviderID+rule.SourceName, "*?[") {
				return fmt.Errorf("publication batch actions require exact provider_id and source_name")
			}
			seen := map[string]bool{}
			for _, batch := range rule.RequeuePublicationBatches {
				seen[batch] = true
			}
			for _, batch := range rule.DiscardPublicationBatches {
				if seen[batch] {
					return fmt.Errorf("cannot requeue and discard the same batch")
				}
			}
		}
		if rule.StopPublicationRetention && retained[key(rule.Schema, rule.ProviderID, rule.SourceName)] {
			return fmt.Errorf("stop_publication_retention and retain_for_publication cannot both be true")
		}
		if len(rule.CatalogExcludeCIDs) > 0 && (strings.TrimSuffix(rule.Schema, ".fbs") != "CAT" || !retained[key(rule.Schema, rule.ProviderID, rule.SourceName)]) {
			return fmt.Errorf("catalog_exclude_cids requires an explicitly retained CAT source")
		}
		if !rule.PairedPublication && !rule.RetainForPublication && rule.CatalogProductSource == "" {
			continue
		}
		if !c.Publishing.Enabled {
			return fmt.Errorf("publication retention and paired_publication require publishing.enabled: true; disable these options to stop automatic publication")
		}
		if strings.TrimSpace(rule.ProviderID) == "" || strings.TrimSpace(rule.SourceName) == "" || strings.ContainsAny(rule.ProviderID+rule.SourceName+rule.CatalogProductSource, "*?[") {
			return fmt.Errorf("publication options require exact provider_id and source_name; remove wildcard rules")
		}
		schema := strings.TrimSuffix(strings.TrimSpace(rule.Schema), ".fbs")
		if rule.RetainForPublication {
			batch := false
			for _, candidate := range c.Publishing.AutoPublish {
				if strings.ToLower(strings.TrimSpace(candidate.PublishScope)) == "source" || strings.ToUpper(strings.TrimSuffix(strings.ToLower(strings.TrimSpace(candidate.Schema)), ".fbs")) != schema {
					continue
				}
				provider, source := strings.TrimSpace(candidate.ProviderID), strings.TrimSpace(candidate.SourceName)
				if (provider == "" || strings.EqualFold(provider, strings.TrimSpace(rule.ProviderID))) && (source == "" || strings.EqualFold(source, strings.TrimSpace(rule.SourceName))) {
					batch = true
					break
				}
			}
			if !batch {
				return fmt.Errorf("retain_for_publication requires a matching batch rule to drain retained entries")
			}
			if schema != "CAT" && schema != "OCM" {
				return fmt.Errorf("retain_for_publication supports only CAT and OCM")
			}
			if f := strings.ToLower(strings.TrimSpace(os.Getenv("SDN_STORE_FORMAT"))); f != "" && f != "4" && f != "sqlite" {
				return fmt.Errorf("retain_for_publication requires SDN_STORE_FORMAT=4")
			}
		}
		if rule.CatalogProductSource != "" {
			if schema != "CAT" || !retained[key(rule.Schema, rule.ProviderID, rule.SourceName)] || !retained[key("OCM", rule.ProviderID, rule.CatalogProductSource)] {
				return fmt.Errorf("catalog_product_source requires explicit retain_for_publication on both CAT and OCM sources")
			}
		}
		if keep, _ := c.Publishing.Retention.PolicyFor(rule.Schema, rule.ProviderID, rule.SourceName); keep != 0 {
			return fmt.Errorf("paired or retained publication requires publishing.retention.keep_series: 0, including matching overrides")
		}
	}
	return nil
}

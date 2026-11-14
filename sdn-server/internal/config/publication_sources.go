package config

import (
	"fmt"
	"os"
	"strings"
)

func (c *Config) validatePublicationSources() error {
	retained := map[string]bool{}
	key := func(schema, provider, source string) string {
		return strings.TrimSuffix(strings.TrimSpace(schema), ".fbs") + "/" + strings.TrimSpace(provider) + "/" + strings.TrimSpace(source)
	}
	for _, rule := range c.Publishing.AutoPublish {
		if rule.RetainForPublication {
			retained[key(rule.Schema, rule.ProviderID, rule.SourceName)] = true
		}
	}
	for _, rule := range c.Publishing.AutoPublish {
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

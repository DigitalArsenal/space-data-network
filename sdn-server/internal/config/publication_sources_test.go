package config

import (
	"strings"
	"testing"
)

func TestPublicationSourceConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"legacy", func(c *Config) { c.Publishing.AutoPublish = nil }, ""},
		{"retained", func(c *Config) {}, ""},
		{"disabled", func(c *Config) { c.Publishing.Enabled = false }, "publishing.enabled"},
		{"wildcard", func(c *Config) { c.Publishing.AutoPublish[0].SourceName = "*" }, "exact"},
		{"schema", func(c *Config) { c.Publishing.AutoPublish[0].Schema = "OMM" }, "CAT and OCM"},
		{"series", func(c *Config) { c.Publishing.Retention.KeepSeries = 2 }, "keep_series"},
		{"override", func(c *Config) {
			c.Publishing.Retention.Lanes = []PublicationRetentionLane{{Schema: "CAT", KeepSeries: 1}}
		}, "keep_series"},
		{"implicit", func(c *Config) {
			c.Publishing.AutoPublish[0].RetainForPublication = false
			c.Publishing.AutoPublish[0].CatalogProductSource = "products"
		}, "explicit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			c.Publishing.Enabled = true
			c.Publishing.Retention.KeepSeries = 0
			c.Publishing.AutoPublish = []AutoPublishLane{{Schema: "CAT", ProviderID: "synthetic", SourceName: "index", RetainForPublication: true}}
			tc.mutate(c)
			err := c.validatePublicationSources()
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v want %s", err, tc.want)
			}
		})
	}
}

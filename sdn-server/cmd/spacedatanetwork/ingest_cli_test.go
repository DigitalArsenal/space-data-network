package main

import "testing"

func TestIngestCommandExposesAllCelestrakSourceFlags(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"celestrak-catalog-url",
		"celestrak-satcat-url",
		"celestrak-satcat-csv-url",
		"celestrak-space-weather-url",
		"celestrak-space-weather-interval",
		"dataset-publish-url",
	} {
		if ingestCmd.Flags().Lookup(name) == nil {
			t.Fatalf("ingest flag %q is not registered", name)
		}
	}
}

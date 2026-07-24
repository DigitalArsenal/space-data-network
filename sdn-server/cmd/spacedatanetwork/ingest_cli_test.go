package main

import "testing"

func TestIngestCommandHasNoDirectCelesTrakSourceFlags(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"celestrak-interval",
		"satcat-interval",
		"celestrak-catalog-url",
		"celestrak-satcat-url",
		"celestrak-satcat-csv-url",
		"celestrak-space-weather-url",
		"celestrak-space-weather-interval",
		"dataset-publish-url",
	} {
		if ingestCmd.Flags().Lookup(name) != nil {
			t.Fatalf("production ingest command exposes forbidden direct source flag %q", name)
		}
	}
}

func TestIngestCommandExposesAllUDLFlags(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"udl-enabled",
		"udl-username",
		"udl-password",
		"udl-base-url",
		"udl-start-day",
		"udl-batch-days",
		"udl-batch-sleep",
		"udl-poll-interval",
		"udl-max-results",
	} {
		if ingestCmd.Flags().Lookup(name) == nil {
			t.Fatalf("ingest flag %q is not registered", name)
		}
	}
}

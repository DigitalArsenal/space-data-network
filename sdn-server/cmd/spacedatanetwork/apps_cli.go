package main

// apps — the operator's view of what this node RUNS, and the one control it
// needs over a scheduled app: run it now.
//
// The $APPS feed (GET /api/apps) is anonymous, so 'apps list' needs no session.
// 'apps run' drives a schedule and is Admin-gated, so it signs in through the
// §19 root ceremony like every other gated command — no second authority path.
//
// Why this command exists at all: a timer-served ingest flow's FIRST tick is
// one interval away (3 h for the CelesTrak GP catalog, 24 h for SATCAT). An
// operator who has just installed a bundle needs to see it work now, and a
// deployment needs to be able to prove retrieval without waiting out a day.
// Before this, the run-now route existed on the admin surface with nothing on
// the CLI able to reach it — which on a headless host meant it did not exist.

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

var appsCmd = &cobra.Command{
	Use:   "apps",
	Short: "List the apps this node runs and run their schedules on demand",
	Long: `List the apps this node runs and run their schedules on demand.

An "app" here is a loaded runtime module or a timer-served flow bundle (an
ingest app is the latter). 'apps list' reads the same anonymous $APPS feed a
node board renders, including each source's retrieval metrics: when it was last
pulled, the debounce window it honours, how large the last pull was, and the
last publication notification.`,
}

var appsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List running apps and their retrieval metrics",
	RunE: func(cmd *cobra.Command, _ []string) error {
		// ANONYMOUS BY CONTRACT. The $APPS feed is on the node's public read
		// surface, so an operator inspecting a node must not be blocked by a
		// sign-in they do not need — e.g. on a host where the seed is readable
		// only by another user, or on a node they do not own. Sign in when we
		// can (a session costs nothing and keeps the audit trail), fall back to
		// an anonymous read when we cannot, and SAY SO rather than pretending.
		client, err := newAdminClient(cmd)
		if err != nil {
			anon, aerr := newAnonymousAdminClient()
			if aerr != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "note: reading the apps feed anonymously (%v)\n",
				firstLine(err.Error()))
			client = anon
		}
		var feed struct {
			GeneratedAt string `json:"generated_at"`
			Apps        []struct {
				ID            string `json:"id"`
				Name          string `json:"name"`
				Kind          string `json:"kind"`
				Status        string `json:"status"`
				RunCount      uint64 `json:"run_count"`
				ErrorCount    uint64 `json:"error_count"`
				LastRunAt     string `json:"last_run_at"`
				LastRunStatus string `json:"last_run_status"`
				Timers        []struct {
					TriggerID     string  `json:"trigger_id"`
					IntervalHours float64 `json:"interval_hours"`
				} `json:"timers"`
				Sources []struct {
					SourceID          string `json:"source_id"`
					LastRetrievedAt   string `json:"last_retrieved_at"`
					LastPullSizeBytes int64  `json:"last_pull_size_bytes"`
					LastRecords       int    `json:"last_records"`
					LastInserted      int    `json:"last_inserted"`
				} `json:"sources"`
			} `json:"apps"`
		}
		if err := client.get(cmd.Context(), "/api/apps", &feed); err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		if len(feed.Apps) == 0 {
			fmt.Fprintln(out, "no apps loaded")
			return nil
		}
		for _, app := range feed.Apps {
			name := app.Name
			if name == "" {
				name = app.ID
			}
			fmt.Fprintf(out, "%s  [%s] %s\n", name, app.Kind, app.Status)
			fmt.Fprintf(out, "  id          %s\n", app.ID)
			fmt.Fprintf(out, "  runs        %d (errors %d)", app.RunCount, app.ErrorCount)
			if app.LastRunAt != "" {
				fmt.Fprintf(out, "  last %s %s", app.LastRunAt, app.LastRunStatus)
			} else if app.LastRunStatus != "" {
				fmt.Fprintf(out, "  %s", app.LastRunStatus)
			}
			fmt.Fprintln(out)
			for _, timer := range app.Timers {
				fmt.Fprintf(out, "  timer       %s every %gh\n", timer.TriggerID, timer.IntervalHours)
			}
			for _, src := range app.Sources {
				retrieved := src.LastRetrievedAt
				if retrieved == "" {
					retrieved = "never"
				}
				fmt.Fprintf(out, "  source      %s  last %s  %d bytes  %d records (%d new)\n",
					src.SourceID, retrieved, src.LastPullSizeBytes, src.LastRecords, src.LastInserted)
			}
			fmt.Fprintln(out)
		}
		return nil
	},
}

var appsRunCmd = &cobra.Command{
	Use:   "run <app-id> [trigger-id]",
	Short: "Run an app's schedule now instead of waiting for its next tick",
	Long: `Run an app's schedule now.

With no trigger-id, every timer the app declares is run in order — which for an
ingest app is exactly one retrieval cycle per source. Each run is synchronous:
the command returns when the app has finished, so a non-zero exit means the pull
actually failed rather than merely failing to start.`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newAdminClient(cmd)
		if err != nil {
			return err
		}
		appID := strings.TrimSpace(args[0])
		out := cmd.OutOrStdout()

		triggers := []string{}
		if len(args) == 2 {
			triggers = append(triggers, strings.TrimSpace(args[1]))
		} else {
			discovered, err := appTriggerIDs(cmd, client, appID)
			if err != nil {
				return err
			}
			if len(discovered) == 0 {
				return fmt.Errorf("app %q declares no timers to run", appID)
			}
			triggers = discovered
		}

		var failures []string
		for _, trigger := range triggers {
			path := fmt.Sprintf("/api/v1/modules/runtime/%s/schedules/%s/run",
				pathEscapeSegment(appID), pathEscapeSegment(trigger))
			var run struct {
				Status     string `json:"status"`
				Message    string `json:"message"`
				StartedAt  string `json:"startedAt"`
				FinishedAt string `json:"finishedAt"`
				OutputSize int    `json:"outputSize"`
			}
			if err := client.postJSON(cmd.Context(), path, nil, &run, ""); err != nil {
				failures = append(failures, fmt.Sprintf("%s: %v", trigger, err))
				fmt.Fprintf(out, "%s %s: FAILED: %v\n", appID, trigger, err)
				continue
			}
			fmt.Fprintf(out, "%s %s: %s  %s -> %s  (%d bytes of results)\n",
				appID, trigger, run.Status, run.StartedAt, run.FinishedAt, run.OutputSize)
			if run.Status != "ok" {
				failures = append(failures, fmt.Sprintf("%s: %s", trigger, run.Message))
			}
		}
		if len(failures) > 0 {
			return fmt.Errorf("%d of %d runs failed: %s",
				len(failures), len(triggers), strings.Join(failures, "; "))
		}
		return nil
	},
}

// appTriggerIDs reads the app's declared timers from the $APPS feed so
// 'apps run <id>' needs no knowledge of a flow's internal trigger names.
func appTriggerIDs(cmd *cobra.Command, client *adminClient, appID string) ([]string, error) {
	var feed struct {
		Apps []struct {
			ID     string `json:"id"`
			Timers []struct {
				TriggerID string `json:"trigger_id"`
			} `json:"timers"`
		} `json:"apps"`
	}
	if err := client.get(cmd.Context(), "/api/apps", &feed); err != nil {
		return nil, err
	}
	known := make([]string, 0, len(feed.Apps))
	for _, app := range feed.Apps {
		known = append(known, app.ID)
		if app.ID != appID {
			continue
		}
		triggers := make([]string, 0, len(app.Timers))
		for _, timer := range app.Timers {
			triggers = append(triggers, timer.TriggerID)
		}
		return triggers, nil
	}
	return nil, fmt.Errorf("app %q is not loaded on this node; loaded apps: %s",
		appID, strings.Join(known, ", "))
}

// pathEscapeSegment escapes one path segment. App ids are dotted reverse-DNS
// names and trigger ids are plain, but neither is validated input from this
// process's point of view, and the route parses segments positionally.
func pathEscapeSegment(segment string) string {
	return strings.ReplaceAll(url.PathEscape(segment), "/", "%2F")
}

func init() {
	appsCmd.AddCommand(appsListCmd)
	appsCmd.AddCommand(appsRunCmd)
	rootCmd.AddCommand(appsCmd)
}

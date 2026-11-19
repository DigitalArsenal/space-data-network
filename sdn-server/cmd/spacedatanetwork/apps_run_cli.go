package main

// apps run / status / cancel — an operator's manual run of an app's schedule.
//
// The run belongs to the node (plugins/manual_runs.go): the command starts it,
// then waits for it by asking the node for its state, a short request at a
// time. No request lives as long as the run, so no client or server timeout
// can cut it, and the command can stop waiting (its --timeout, Ctrl-C, a lost
// connection) while the run continues on the node. host-01, 2026-11-17: the
// command's single request gave up after its 60 s client timeout, and the node
// then cancelled the run with it.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

// appRun is the node's state of one manual run.
type appRun struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Message    string `json:"message"`
	StartedAt  string `json:"startedAt"`
	FinishedAt string `json:"finishedAt"`
	OutputSize int    `json:"outputSize"`
	Joined     bool   `json:"joined"`
	// ForceNotApplied: this command asked for a forced run and joined a
	// running one that was not forced; the force did not apply.
	ForceNotApplied bool `json:"forceNotApplied"`
}

var (
	appsRunTimeout           time.Duration
	appsRunDetach            bool
	appsRunCancelOnInterrupt bool
	appsRunForce             bool
)

// appsRunPoll is how often the command asks the node for the run's state;
// appsRunProgress how often it says the run is still going.
var (
	appsRunPoll     = 2 * time.Second
	appsRunProgress = 30 * time.Second
)

var appsRunCmd = &cobra.Command{
	Use:   "run <app-id> [trigger-id]",
	Short: "Run an app's schedule now instead of waiting for its next tick",
	Long: `Run an app's schedule now.

With no trigger-id, every timer the app declares is run in order, which for an
ingest app is one retrieval cycle per source.

The run belongs to the node, not to this command: the node carries it to its
end whatever the command does. The command waits for each run and reports its
end, saying every 30 s that it is still running; a non-zero exit means a run
failed, or that the command stopped waiting before a run ended.

  --timeout D             wait at most D in all (0, the default: until the end).
                          When D passes, the command says how to see the result
                          and exits non-zero; the run continues.
  --detach                start the runs and return at once.
  Ctrl-C                  stop waiting; the run continues on the node.
  --cancel-on-interrupt   Ctrl-C cancels the run instead.
  --force                 run even inside the provider's minimum interval.
                          A run already running is joined and keeps its own
                          input: the command then says --force did not apply.

The command follows the run it started or joined by its id. When the node no
longer knows that run (a restart before the run was recorded), the command
says so and exits non-zero; it never reports another run's result.

'apps status' shows a run's state and result; 'apps cancel' cancels a run.
A run of a schedule that is already running is joined, not started again.`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		client, err := newAdminClient(cmd)
		if err != nil {
			return err
		}
		appID := strings.TrimSpace(args[0])
		triggers, err := appRunTriggers(cmd, client, appID, args)
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		ctx, stopSignals := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stopSignals()
		var deadline time.Time
		if appsRunTimeout > 0 {
			deadline = time.Now().Add(appsRunTimeout)
		}

		var failures []string
		for i, trigger := range triggers {
			run, err := startAppRun(ctx, client, appID, trigger)
			if err != nil {
				failures = append(failures, fmt.Sprintf("%s: %v", trigger, err))
				fmt.Fprintf(out, "%s %s: FAILED to start: %v\n", appID, trigger, err)
				continue
			}
			if run.Joined {
				fmt.Fprintf(out, "%s %s: already running since %s (run %s); this command follows that run\n", appID, trigger, run.StartedAt, run.ID)
			} else {
				fmt.Fprintf(out, "%s %s: started %s (run %s)\n", appID, trigger, run.StartedAt, run.ID)
			}
			if run.ForceNotApplied {
				fmt.Fprintf(out, "%s %s: --force does not apply: the running run was started without it and keeps its own input\n", appID, trigger)
			}
			if run.Status == "running" && run.ID == "" && !appsRunDetach {
				failures = append(failures, fmt.Sprintf("%s: the node's answer names no run id", trigger))
				fmt.Fprintf(out, "%s %s: the node's answer names no run id, so this command cannot follow the run\n", appID, trigger)
				continue
			}
			if appsRunDetach {
				fmt.Fprintf(out, "%s %s: runs on the node; see it with: spacedatanetwork apps status %s %s\n", appID, trigger, appID, trigger)
				continue
			}
			// A node may answer with the run already ended.
			final, stopped := run, ""
			if run.Status == "running" {
				final, stopped = waitAppRun(ctx, cmd, client, out, appID, trigger, run.ID, deadline)
			}
			if stopped != "" {
				left := triggers[i+1:]
				msg := fmt.Sprintf("%s %s: %s", appID, trigger, stopped)
				if len(left) > 0 {
					msg += fmt.Sprintf("; not started: %s", strings.Join(left, ", "))
				}
				return errors.New(msg)
			}
			fmt.Fprintf(out, "%s %s: %s  %s -> %s  (%d bytes of results)\n",
				appID, trigger, final.Status, final.StartedAt, final.FinishedAt, final.OutputSize)
			if final.Status != "ok" {
				fmt.Fprintf(out, "  %s\n", final.Message)
				failures = append(failures, fmt.Sprintf("%s: %s", trigger, final.Message))
			}
		}
		if len(failures) > 0 {
			return fmt.Errorf("%d of %d runs failed: %s",
				len(failures), len(triggers), strings.Join(failures, "; "))
		}
		return nil
	},
}

var appsStatusCmd = &cobra.Command{
	Use:   "status <app-id> [trigger-id]",
	Short: "Show the running or last manual run of an app's schedules",
	Long: `Show the running or last manual run of each of an app's schedules (or of
one): its state (running, ok, error, deferred), its start and end, and its
message. A run that ended since the node started is shown with its result; an
earlier one from the schedule's run history.`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		client, err := newAdminClient(cmd)
		if err != nil {
			return err
		}
		appID := strings.TrimSpace(args[0])
		triggers, err := appRunTriggers(cmd, client, appID, args)
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		for _, trigger := range triggers {
			var run appRun
			if err := client.get(cmd.Context(), appRunPath(appID, trigger), &run); err != nil {
				if strings.Contains(err.Error(), "404") {
					fmt.Fprintf(out, "%s %s: no manual run\n", appID, trigger)
					continue
				}
				return err
			}
			printAppRun(out, appID, trigger, run)
		}
		return nil
	},
}

var appsCancelCmd = &cobra.Command{
	Use:   "cancel <app-id> [trigger-id]",
	Short: "Cancel the running manual run of an app's schedules",
	Long: `Cancel the running manual run of each of an app's schedules (or of one) and
show how it ended. A schedule with no running manual run is reported and left
alone.`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		client, err := newAdminClient(cmd)
		if err != nil {
			return err
		}
		appID := strings.TrimSpace(args[0])
		triggers, err := appRunTriggers(cmd, client, appID, args)
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		for _, trigger := range triggers {
			run, err := cancelAppRun(cmd.Context(), client, appID, trigger, "")
			if err != nil {
				if strings.Contains(err.Error(), "409") {
					fmt.Fprintf(out, "%s %s: no running manual run\n", appID, trigger)
					continue
				}
				return err
			}
			printAppRun(out, appID, trigger, run)
		}
		return nil
	},
}

func appRunPath(appID, trigger string) string {
	return fmt.Sprintf("/api/v1/modules/runtime/%s/schedules/%s/run", pathEscapeSegment(appID), pathEscapeSegment(trigger))
}

// appRunTriggers is the trigger the arguments name, or every timer the app
// declares.
func appRunTriggers(cmd *cobra.Command, client *adminClient, appID string, args []string) ([]string, error) {
	if len(args) == 2 {
		return []string{strings.TrimSpace(args[1])}, nil
	}
	discovered, err := appTriggerIDs(cmd, client, appID)
	if err != nil {
		return nil, err
	}
	if len(discovered) == 0 {
		return nil, fmt.Errorf("app %q declares no timers to run", appID)
	}
	return discovered, nil
}

func startAppRun(ctx context.Context, client *adminClient, appID, trigger string) (appRun, error) {
	var run appRun
	var input any
	if appsRunForce {
		input = map[string]bool{"force": true}
	}
	err := client.postJSON(ctx, appRunPath(appID, trigger)+"?detach=1", input, &run, "")
	return run, err
}

// cancelAppRun cancels run runID of the schedule, or, with runID "", the
// running one. The node answers 404 when runID is not the run running.
func cancelAppRun(ctx context.Context, client *adminClient, appID, trigger, runID string) (appRun, error) {
	var run appRun
	path := appRunPath(appID, trigger)
	if runID != "" {
		path += "?id=" + url.QueryEscape(runID)
	}
	err := client.do(ctx, "DELETE", path, nil, &run)
	return run, err
}

func printAppRun(out io.Writer, appID, trigger string, run appRun) {
	if run.Status == "running" {
		fmt.Fprintf(out, "%s %s: running since %s\n", appID, trigger, run.StartedAt)
		return
	}
	fmt.Fprintf(out, "%s %s: %s  %s -> %s  (%d bytes of results)\n",
		appID, trigger, run.Status, run.StartedAt, run.FinishedAt, run.OutputSize)
	if run.Message != "" {
		fmt.Fprintf(out, "  %s\n", run.Message)
	}
}

// waitAppRun asks the node for the state of run runID until it ends. It
// answers the ended run, or, when the command stopped waiting first (its
// deadline, Ctrl-C, the node out of reach, the node no longer knowing the
// run), why, with the run left running on the node (or cancelled, with
// --cancel-on-interrupt). It follows runID only: another run of the
// schedule, running or ended, is never taken for it.
func waitAppRun(ctx context.Context, cmd *cobra.Command, client *adminClient, out io.Writer, appID, trigger, runID string, deadline time.Time) (appRun, string) {
	started := time.Now()
	lastProgress := started
	see := fmt.Sprintf("see its result with: spacedatanetwork apps status %s %s", appID, trigger)
	failedPolls := 0
	for {
		wait := appsRunPoll
		if !deadline.IsZero() {
			if left := time.Until(deadline); left <= 0 {
				return appRun{}, fmt.Sprintf("still running on the node after %s; this command stopped waiting (--timeout %s) and the run continues; %s",
					time.Since(started).Round(time.Second), appsRunTimeout, see)
			} else if left < wait {
				wait = left
			}
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			if appsRunCancelOnInterrupt {
				cancelCtx, cancel := context.WithTimeout(context.Background(), moduleScheduleCancelWait+10*time.Second)
				defer cancel()
				run, err := cancelAppRun(cancelCtx, client, appID, trigger, runID)
				if err != nil && strings.Contains(err.Error(), " 404 ") {
					return appRun{}, fmt.Sprintf("interrupted; run %s was not cancelled: it is no longer running on the node; %s", runID, see)
				}
				if err != nil {
					return appRun{}, fmt.Sprintf("interrupted; cancelling the run failed: %v; %s", err, see)
				}
				return appRun{}, fmt.Sprintf("interrupted; the run was cancelled: %s %s", run.Status, run.Message)
			}
			return appRun{}, fmt.Sprintf("interrupted; this command stopped waiting and the run continues on the node; %s; cancel it with: spacedatanetwork apps cancel %s %s",
				see, appID, trigger)
		case <-timer.C:
		}
		var run appRun
		if err := client.get(ctx, appRunPath(appID, trigger)+"?id="+url.QueryEscape(runID), &run); err != nil {
			if ctx.Err() != nil {
				continue
			}
			if strings.Contains(err.Error(), " 404 ") {
				return appRun{}, fmt.Sprintf("the node no longer knows run %s (it may have restarted before the run ended and was recorded); its result is unknown; see the schedule's runs with: spacedatanetwork apps status %s %s",
					runID, appID, trigger)
			}
			// A session can expire during a long run: sign in again once
			// per failure before giving up.
			failedPolls++
			if failedPolls > 3 {
				return appRun{}, fmt.Sprintf("the node did not answer (%v); this command stopped waiting and the run continues on the node; %s", err, see)
			}
			if fresh, ferr := newAdminClient(cmd); ferr == nil {
				*client = *fresh
			}
			continue
		}
		failedPolls = 0
		if run.ID != runID {
			return appRun{}, fmt.Sprintf("the node answered run %q for run %s; its result is unknown; see the schedule's runs with: spacedatanetwork apps status %s %s",
				run.ID, runID, appID, trigger)
		}
		if run.Status != "running" {
			return run, ""
		}
		if time.Since(lastProgress) >= appsRunProgress {
			lastProgress = time.Now()
			fmt.Fprintf(out, "%s %s: still running (%s)\n", appID, trigger, time.Since(started).Round(time.Second))
		}
	}
}

func init() {
	appsRunCmd.Flags().DurationVar(&appsRunTimeout, "timeout", 0, "how long this command waits for the runs in all (0: until they end); the runs continue after it")
	appsRunCmd.Flags().BoolVar(&appsRunDetach, "detach", false, "start the runs and return at once")
	appsRunCmd.Flags().BoolVar(&appsRunCancelOnInterrupt, "cancel-on-interrupt", false, "Ctrl-C cancels the run instead of only stopping the wait")
	appsRunCmd.Flags().BoolVar(&appsRunForce, "force", false, "run even inside the provider's minimum interval (input {\"force\":true}); a run already running is joined and keeps its own input")
	appsCmd.AddCommand(appsStatusCmd)
	appsCmd.AddCommand(appsCancelCmd)
}

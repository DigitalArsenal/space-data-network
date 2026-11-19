package main

// sync subscribe / unsubscribe / subscriptions: a node imports only the
// lanes it subscribes to (1.2.0). Every command goes through the running
// daemon's admin API, signed in with the node's root key the way `apps run`
// signs in (admin_client.go): no credential passes through the operator.
//
// A subscription's first read is bounded by the lane's rule: a
// replace-current lane starts at the producer's current batch, a keep-all or
// archive-all lane at its beginning (--start overrides). Before it starts,
// the command shows the producer's statement of that first read (records and
// bytes) and the node refuses it when its free disk or its record ceiling
// cannot take it, unless --force.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/spacedatanetwork/sdn-server/internal/api"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

type syncSubscribeOptions struct {
	Schema, ProviderID, SourceName, From, Start string
	Force, JSON, FromLedger, Yes, Acknowledge   bool
}

var (
	syncSubscribeFlags   syncSubscribeOptions
	syncUnsubscribeFlags syncSubscribeOptions
	syncListJSON         bool
)

// syncSubscribeTimeout bounds one plan or subscribe call: the daemon reads
// the producer's statement (at most a minute) and counts its store.
const syncSubscribeTimeout = 5 * time.Minute

var syncSubscribeCmd = &cobra.Command{
	Use:   "subscribe",
	Short: "Subscribe to a lane from a producer peer (the only way a node imports a lane)",
	Long: `Subscribe this node to a lane (standard, provider, source) from the producer
peer that serves it. The node follows the lane over a live stream.

The first read is bounded by the lane's rule: a replace-current lane starts at
the producer's current batch, a keep-all or archive-all lane at its beginning
(--start current|beginning overrides). The command prints the producer's size
of that first read, and the node refuses it when its free disk or its record
ceiling (subscriptions.record_ceiling) cannot take it, unless --force.

--from-ledger converts the lanes of the 1.1.9 import ledger and
peers.trusted_import allow list into subscriptions that start at each
producer's current batch: it prints the plan and acts only with --yes.

When a producer refuses the saved cursor of a lane whose history this node
keeps, the lane starts again at the producer's current batch and the records
between were not read; sync subscriptions shows it as "not read" until
--start beginning reads the whole lane again or --acknowledge-unread accepts
the gap.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if syncSubscribeFlags.FromLedger {
			return runSyncSubscribeFromLedger(cmd, syncSubscribeFlags)
		}
		if syncSubscribeFlags.Acknowledge {
			return runSyncAcknowledgeUnread(cmd, syncSubscribeFlags)
		}
		return runSyncSubscribe(cmd, syncSubscribeFlags)
	},
}

var syncUnsubscribeCmd = &cobra.Command{
	Use:   "unsubscribe",
	Short: "Stop following a lane",
	Long: `Stop following a lane: from one producer (--from), or from every producer.
With --schema alone, every followed lane of the standard stops. Records already
stored stay.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runSyncUnsubscribe(cmd, syncUnsubscribeFlags)
	},
}

var syncSubscriptionsCmd = &cobra.Command{
	Use:   "subscriptions",
	Short: "List the lanes this node follows, with their state",
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runSyncSubscriptions(cmd, syncListJSON)
	},
}

func init() {
	f := syncSubscribeCmd.Flags()
	f.StringVar(&syncSubscribeFlags.Schema, "schema", "", "standard, for example OMM")
	f.StringVar(&syncSubscribeFlags.ProviderID, "provider-id", "", "the lane's provider")
	f.StringVar(&syncSubscribeFlags.SourceName, "source-name", "", "the lane's source")
	f.StringVar(&syncSubscribeFlags.From, "from", "", "the producer's peer ID")
	f.StringVar(&syncSubscribeFlags.Start, "start", "", "where the first read starts: current (the producer's current batch) or beginning; default by the lane's rule")
	f.BoolVar(&syncSubscribeFlags.Force, "force", false, "subscribe although the first read does not fit this node or the producer does not state the lane")
	f.BoolVar(&syncSubscribeFlags.FromLedger, "from-ledger", false, "subscribe to every lane of the 1.1.9 import ledger and allow list")
	f.BoolVar(&syncSubscribeFlags.Yes, "yes", false, "with --from-ledger: subscribe (without it the plan is printed only)")
	f.BoolVar(&syncSubscribeFlags.JSON, "json", false, "print machine-readable JSON")
	f.BoolVar(&syncSubscribeFlags.Acknowledge, "acknowledge-unread", false, "accept the records a refused cursor left unread on this subscription (subscribes nothing)")
	addSessionTokenFlag(syncSubscribeCmd)

	u := syncUnsubscribeCmd.Flags()
	u.StringVar(&syncUnsubscribeFlags.Schema, "schema", "", "standard, for example OMM")
	u.StringVar(&syncUnsubscribeFlags.ProviderID, "provider-id", "", "the lane's provider (with --source-name; omit both for every lane of the standard)")
	u.StringVar(&syncUnsubscribeFlags.SourceName, "source-name", "", "the lane's source")
	u.StringVar(&syncUnsubscribeFlags.From, "from", "", "the producer's peer ID (omit for every producer)")
	u.BoolVar(&syncUnsubscribeFlags.JSON, "json", false, "print machine-readable JSON")
	addSessionTokenFlag(syncUnsubscribeCmd)

	syncSubscriptionsCmd.Flags().BoolVar(&syncListJSON, "json", false, "print machine-readable JSON")
	addSessionTokenFlag(syncSubscriptionsCmd)

	syncCmd.AddCommand(syncSubscribeCmd, syncUnsubscribeCmd, syncSubscriptionsCmd)
}

// syncAPICall sends one request to the daemon and decodes a JSON answer
// into out. A refusal is the error frame's message.
func syncAPICall(ctx context.Context, c *adminClient, method, path, contentType string, body []byte, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := c.newRequest(ctx, method, path, reader)
	if err != nil {
		return 0, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json, "+publicationStreamMediaType)
	client := *c.http
	client.Timeout = syncSubscribeTimeout
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%s %s: daemon unavailable at %s: %w", method, path, c.baseURL, err)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, errors.New(refusalMessage(resp.Status, payload))
	}
	if out != nil && len(bytes.TrimSpace(payload)) > 0 {
		if err := json.Unmarshal(payload, out); err != nil {
			return resp.StatusCode, fmt.Errorf("%s %s: %w", method, path, err)
		}
	}
	return resp.StatusCode, nil
}

// publicationStreamMediaType is the FlatBuffers stream the $DSS routes speak.
const publicationStreamMediaType = "application/vnd.sdn.flatbuffers.stream"

// refusalMessage is the message of an error frame (a size-prefixed $QRP),
// else the body as text.
func refusalMessage(status string, payload []byte) string {
	if frames, err := api.ReadFrames(bytes.NewReader(payload), int64(len(payload))+1); err == nil && len(frames) > 0 {
		if q, err := syncwire.ParseQRP(frames[0]); err == nil && len(q.MESSAGE()) > 0 {
			return string(q.MESSAGE())
		}
	}
	return status + ": " + strings.TrimSpace(string(payload))
}

func planQuery(o syncSubscribeOptions) string {
	q := url.Values{}
	q.Set("schema", o.Schema)
	q.Set("provider_id", o.ProviderID)
	q.Set("source", o.SourceName)
	q.Set("from", o.From)
	if o.Start != "" {
		q.Set("start", o.Start)
	}
	return q.Encode()
}

func requireLane(o syncSubscribeOptions) error {
	var missing []string
	for _, f := range []struct{ name, value string }{{"--schema", o.Schema}, {"--provider-id", o.ProviderID}, {"--source-name", o.SourceName}, {"--from", o.From}} {
		if strings.TrimSpace(f.value) == "" {
			missing = append(missing, f.name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("a subscription names %s", strings.Join(missing, ", "))
	}
	return nil
}

// subscribeOne plans and, when it fits or force, subscribes.
func subscribeOne(ctx context.Context, c *adminClient, o syncSubscribeOptions) (api.LanePlan, bool, error) {
	var plan api.LanePlan
	if _, err := syncAPICall(ctx, c, http.MethodGet, api.SyncPlanPath+"?"+planQuery(o), "", nil, &plan); err != nil {
		return plan, false, err
	}
	if plan.Refusal != "" && !o.Force {
		return plan, false, nil
	}
	q := url.Values{}
	q.Set("start", plan.Start)
	if o.Force {
		q.Set("force", "1")
	}
	if _, err := syncAPICall(ctx, c, http.MethodPost, api.SyncPath+"?"+q.Encode(), publicationStreamMediaType,
		api.EncodeDSSFollow(o.Schema, o.ProviderID, o.SourceName, o.From), nil); err != nil {
		return plan, false, err
	}
	return plan, true, nil
}

func runSyncSubscribe(cmd *cobra.Command, o syncSubscribeOptions) error {
	if err := requireLane(o); err != nil {
		return err
	}
	c, err := newAdminClient(cmd)
	if err != nil {
		return err
	}
	plan, subscribed, err := subscribeOne(cmd.Context(), c, o)
	if err != nil {
		return err
	}
	if o.JSON {
		if err := printJSON(cmd, map[string]any{"plan": plan, "subscribed": subscribed}); err != nil {
			return err
		}
	} else {
		writePlan(cmd.OutOrStdout(), plan)
		if subscribed {
			fmt.Fprintf(cmd.OutOrStdout(), "subscribed: %s %s/%s from %s\n", plan.Schema, plan.ProviderID, plan.SourceName, plan.Producer)
		}
	}
	if !subscribed {
		return fmt.Errorf("not subscribed: %s (--force subscribes anyway)", plan.Refusal)
	}
	return nil
}

// runSyncAcknowledgeUnread clears what a refused cursor left unread on one
// subscription.
func runSyncAcknowledgeUnread(cmd *cobra.Command, o syncSubscribeOptions) error {
	if err := requireLane(o); err != nil {
		return err
	}
	c, err := newAdminClient(cmd)
	if err != nil {
		return err
	}
	if _, err := syncAPICall(cmd.Context(), c, http.MethodPost, api.SyncPath+"?acknowledge_unread=1", publicationStreamMediaType,
		api.EncodeDSSFollow(o.Schema, o.ProviderID, o.SourceName, o.From), nil); err != nil {
		return err
	}
	if o.JSON {
		return printJSON(cmd, map[string]any{"acknowledged": true, "schema": o.Schema, "provider_id": o.ProviderID, "source_name": o.SourceName, "producer": o.From})
	}
	fmt.Fprintf(cmd.OutOrStdout(), "acknowledged: %s %s/%s from %s: the records left unread are no longer reported\n", o.Schema, o.ProviderID, o.SourceName, o.From)
	return nil
}

func writePlan(w io.Writer, p api.LanePlan) {
	fmt.Fprintf(w, "lane:        %s %s/%s\n", p.Schema, p.ProviderID, p.SourceName)
	fmt.Fprintf(w, "producer:    %s\n", p.Producer)
	fmt.Fprintf(w, "rule:        %s\n", p.Rule)
	if p.StatementError != "" {
		fmt.Fprintf(w, "statement:   none (%s)\n", p.StatementError)
	} else {
		fmt.Fprintf(w, "producer:    current batch %s: %d records, %s; lane: %d records, %s\n", p.BatchID, p.BatchRecords, humanBytes(p.BatchBytes), p.LaneRecords, humanBytes(p.LaneBytes))
	}
	where := "the producer's current batch"
	if p.Start == "beginning" {
		where = "the lane's beginning"
	}
	fmt.Fprintf(w, "first read:  %d records, %s (starts at %s)\n", p.FirstReadRecords, humanBytes(p.FirstReadBytes), where)
	if p.StatementError == "" {
		fmt.Fprintf(w, "this node:   %s free disk, %s needed; %d records held, ceiling %d\n", humanBytes(p.FreeBytes), humanBytes(p.NeedBytes), p.HeldRecords, p.RecordCeiling)
	}
	if p.Refusal != "" {
		fmt.Fprintf(w, "refused:     %s\n", p.Refusal)
	}
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func runSyncSubscribeFromLedger(cmd *cobra.Command, o syncSubscribeOptions) error {
	c, err := newAdminClient(cmd)
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	var ledger struct {
		Lanes []api.LedgerLane `json:"lanes"`
		Error string           `json:"error"`
	}
	if _, err := syncAPICall(ctx, c, http.MethodGet, api.SyncLedgerPath, "", nil, &ledger); err != nil {
		return err
	}
	type row struct {
		Lane       api.LedgerLane `json:"lane"`
		Producer   string         `json:"producer,omitempty"`
		Plan       *api.LanePlan  `json:"plan,omitempty"`
		Subscribed bool           `json:"subscribed"`
		Error      string         `json:"error,omitempty"`
	}
	var rows []row
	failed := 0
	for _, lane := range ledger.Lanes {
		if len(lane.Producers) == 0 {
			rows = append(rows, row{Lane: lane})
			continue
		}
		for _, producer := range lane.Producers {
			lo := syncSubscribeOptions{Schema: lane.Schema, ProviderID: lane.ProviderID, SourceName: lane.SourceName, From: producer, Start: "current", Force: o.Force}
			r := row{Lane: lane, Producer: producer}
			var plan api.LanePlan
			if o.Yes {
				plan, r.Subscribed, err = subscribeOne(ctx, c, lo)
			} else {
				_, err = syncAPICall(ctx, c, http.MethodGet, api.SyncPlanPath+"?"+planQuery(lo), "", nil, &plan)
			}
			if err != nil {
				r.Error = err.Error()
				failed++
			} else {
				r.Plan = &plan
				if o.Yes && !r.Subscribed {
					failed++
				}
			}
			rows = append(rows, r)
		}
	}
	if o.JSON {
		if err := printJSON(cmd, map[string]any{"lanes": rows, "ledger_error": ledger.Error, "acted": o.Yes}); err != nil {
			return err
		}
	} else {
		out := cmd.OutOrStdout()
		if ledger.Error != "" {
			fmt.Fprintf(out, "ledger: %s\n", ledger.Error)
		}
		for _, r := range rows {
			l := r.Lane
			switch {
			case r.Producer == "":
				fmt.Fprintf(out, "%s %s/%s: not converted: %s\n", l.Schema, l.ProviderID, l.SourceName, l.Reason)
			case r.Error != "":
				fmt.Fprintf(out, "%s %s/%s from %s: %s\n", l.Schema, l.ProviderID, l.SourceName, r.Producer, r.Error)
			default:
				p := r.Plan
				state := "plan"
				if r.Subscribed {
					state = "subscribed"
				} else if o.Yes {
					state = "not subscribed"
				}
				fmt.Fprintf(out, "%s %s/%s from %s: %s; first read %d records, %s (current batch %s)", l.Schema, l.ProviderID, l.SourceName, r.Producer, state,
					p.FirstReadRecords, humanBytes(p.FirstReadBytes), p.BatchID)
				if p.StatementError != "" {
					fmt.Fprintf(out, "; no statement: %s", p.StatementError)
				}
				if p.Refusal != "" {
					fmt.Fprintf(out, "; refused: %s", p.Refusal)
				}
				fmt.Fprintln(out)
			}
		}
		if !o.Yes {
			fmt.Fprintln(out, "nothing subscribed: run again with --yes to subscribe to each lane above")
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d lanes were not subscribed", failed)
	}
	return nil
}

func runSyncUnsubscribe(cmd *cobra.Command, o syncSubscribeOptions) error {
	if strings.TrimSpace(o.Schema) == "" {
		return errors.New("unsubscribe names --schema")
	}
	c, err := newAdminClient(cmd)
	if err != nil {
		return err
	}
	if _, err := syncAPICall(cmd.Context(), c, http.MethodPost, api.SyncPath, publicationStreamMediaType,
		api.EncodeDSSUnfollow(o.Schema, o.ProviderID, o.SourceName, o.From), nil); err != nil {
		return err
	}
	if o.JSON {
		return printJSON(cmd, map[string]any{"unsubscribed": true, "schema": o.Schema, "provider_id": o.ProviderID, "source_name": o.SourceName, "producer": o.From})
	}
	fmt.Fprintf(cmd.OutOrStdout(), "unsubscribed: %s %s/%s %s\n", o.Schema, o.ProviderID, o.SourceName, o.From)
	return nil
}

type syncSubscriptionList struct {
	GeneratedAt   string           `json:"generated_at"`
	Subscriptions []api.LaneFollow `json:"subscriptions"`
}

func runSyncSubscriptions(cmd *cobra.Command, asJSON bool) error {
	c, err := newAdminClient(cmd)
	if err != nil {
		return err
	}
	var list syncSubscriptionList
	if _, err := syncAPICall(cmd.Context(), c, http.MethodGet, api.SyncSubscriptionsPath, "", nil, &list); err != nil {
		return err
	}
	if asJSON {
		return printJSON(cmd, list)
	}
	out := cmd.OutOrStdout()
	if len(list.Subscriptions) == 0 {
		fmt.Fprintln(out, "no subscriptions: this node imports no lane")
		return nil
	}
	for _, s := range list.Subscriptions {
		fmt.Fprintf(out, "%s %s/%s from %s\n", s.Schema, s.ProviderID, s.SourceName, s.Producer)
		if !s.Since.IsZero() {
			fmt.Fprintf(out, "  subscribed since: %s\n", s.Since.UTC().Format(time.RFC3339))
		}
		switch {
		case s.Start != "":
			fmt.Fprintf(out, "  waiting to start at the producer's current batch\n")
		case s.StartsAt == "beginning":
			fmt.Fprintf(out, "  started at the lane's beginning\n")
		default:
			fmt.Fprintf(out, "  started at the producer's current batch\n")
		}
		if s.Stopped != "" {
			fmt.Fprintf(out, "  stopped: %s\n", s.Stopped)
		}
		if s.Unread != "" {
			fmt.Fprintf(out, "  not read: %s\n", s.Unread)
		}
		state := "connected"
		if !s.Connected {
			state = "not connected"
		}
		fmt.Fprintf(out, "  state: %s; received %d records, %s; stored %d; removed %d\n", state, s.ReceivedRecords, humanBytes(s.ReceivedBytes), s.StoredRecords, s.RemovedRecords)
		fmt.Fprintf(out, "  current batch: %s; producer's batch: %s (%d records); lag: %d records\n", orDash(s.CurrentBatch), orDash(s.StatedBatch), s.StatedRecords, s.Lag)
		if !s.LastReceivedAt.IsZero() {
			fmt.Fprintf(out, "  last record: %s\n", s.LastReceivedAt.UTC().Format(time.RFC3339))
		}
		if s.Error != "" {
			fmt.Fprintf(out, "  last error: %s\n", s.Error)
		}
	}
	return nil
}

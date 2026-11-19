package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/spacedatanetwork/sdn-server/internal/node"
	"github.com/spacedatanetwork/sdn-server/internal/update"
)

// handleUpdateFleet serves GET /api/peers/versions: this node's view of the
// version and update state of every SDN node it knows (node.UpdateFleet).
// Admin only, by the /api/peers prefix (isAdminOnlyAPIPath).
func handleUpdateFleet(n *node.Node) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		data, err := json.Marshal(n.UpdateFleet())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	}
}

var updateFleetJSON bool

var updateFleetCmd = &cobra.Command{
	Use:   "fleet",
	Short: "Show the version and update state of this node and every SDN node it knows",
	Long: "Reads /api/peers/versions from the running daemon. Each node states its own " +
		"version and update state in a signed status message on its update topic (SOURCE status: " +
		"self-reported, not checked by this node); a node whose status has not reached this one " +
		"(a build before 1.2.0, update.publish_status off, a node reached only through others, or " +
		"a status not renewed for 30 minutes) shows its identify version and the state \"not reporting\". " +
		"SIGNAL says whether the node accepted the newest update signal this node verified, or " +
		"already runs its sequence or a later one.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newAdminClient(cmd)
		if err != nil {
			return err
		}
		var report node.FleetReport
		if err := client.get(cmd.Context(), "/api/peers/versions", &report); err != nil {
			return err
		}
		if updateFleetJSON {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(report)
		}
		writeFleetTable(cmd.OutOrStdout(), &report, time.Now())
		return nil
	},
}

func writeFleetTable(out io.Writer, report *node.FleetReport, now time.Time) {
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tPEER\tVERSION\tSOURCE\tLAST SEEN\tUPDATE\tSIGNAL")
	for _, row := range report.Rows {
		name := printable(row.Name, 48)
		if row.Self {
			name = strings.TrimSpace(name + " (this node)")
		}
		if name == "" {
			name = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", name, printable(peerTail(row.PeerID), 11), orDash(printable(row.Version, 64)),
			fleetSource(row), fleetLastSeen(row, now), printable(fleetState(row), 160), fleetSignal(row))
	}
	_ = tw.Flush()
	if report.LatestSignal != nil {
		fmt.Fprintf(out, "latest_signal=%s sequence=%d (the newest signal this node verified)\n", printable(report.LatestSignal.UpdateID, 128), report.LatestSignal.Sequence)
	}
	fmt.Fprintln(out, "SOURCE status: the node's own signed statement, not checked by this node; identify: its identify version only.")
}

// printable is s as one line of plain text for a terminal: every control and
// format character (newline, tab, escape, bidirectional marks) and invalid
// UTF-8 removed, cut at limit runes. Every string another node or the peer
// directory supplies goes through it before it is printed.
func printable(s string, limit int) string {
	var b strings.Builder
	n := 0
	for _, r := range strings.ToValidUTF8(s, "") {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == utf8.RuneError {
			continue
		}
		if n >= limit {
			break
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

func fleetSource(row node.FleetRow) string {
	switch row.Source {
	case "local":
		return "this node"
	case "status":
		return "status"
	case "identify":
		return "identify"
	}
	return "-"
}

func peerTail(id string) string {
	if len(id) > 8 {
		return "..." + id[len(id)-8:]
	}
	return id
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func fleetLastSeen(row node.FleetRow, now time.Time) string {
	if row.Online || row.Self {
		return "now"
	}
	seen, err := time.Parse(time.RFC3339, row.LastSeen)
	if err != nil {
		return "-"
	}
	return now.Sub(seen).Round(time.Second).String() + " ago"
}

func fleetState(row node.FleetRow) string {
	switch row.State {
	case "pending":
		if row.Pending != nil {
			return fmt.Sprintf("pending %s until %s", row.Pending.Version, row.Pending.DueAt)
		}
		return "pending"
	case "failed":
		if row.Failed != nil {
			if row.Failed.RolledBack {
				return fmt.Sprintf("failed %s, rolled back (%s)", row.Failed.Version, row.Failed.ReasonCode)
			}
			return fmt.Sprintf("failed %s, not rolled back (%s)", row.Failed.Version, row.Failed.ReasonCode)
		}
		return "failed"
	case "":
		return node.FleetStateNotReporting
	}
	return row.State
}

func fleetSignal(row node.FleetRow) string {
	if row.AcknowledgedLatest == nil {
		return "-"
	}
	if *row.AcknowledgedLatest {
		return "yes"
	}
	return "no"
}

// writeUpdateRolloutState prints the update this node waits to act on and
// the update failure it reports, read from its update root (`update check`,
// `status`).
func writeUpdateRolloutState(out io.Writer, paths update.Paths) {
	if pending, err := update.CurrentPending(paths); err != nil {
		fmt.Fprintf(out, "update_pending=error error=%q\n", err.Error())
	} else if pending != nil {
		fmt.Fprintf(out, "update_pending=%s version=%s sequence=%d trigger=%s accepted_at=%s due_at=%s window_seconds=%d window_from=%s delay_seconds=%d\n",
			pending.UpdateID, pending.Version, pending.Sequence, pending.Trigger, pending.AcceptedAt, pending.DueAt,
			pending.WindowSeconds, pending.WindowFrom, pending.DelaySeconds)
	} else {
		fmt.Fprintln(out, "update_pending=none")
	}
	if failure := update.ActiveUpdateFailure(paths); failure != nil {
		fmt.Fprintf(out, "update_failed=%s version=%s sequence=%d at=%s rolled_back=%t\n",
			failure.UpdateID, failure.Version, failure.Sequence, failure.At, failure.RolledBack)
	}
}

func init() {
	updateFleetCmd.Flags().BoolVar(&updateFleetJSON, "json", false, "emit the report as JSON")
	updateCmd.AddCommand(updateFleetCmd)
}

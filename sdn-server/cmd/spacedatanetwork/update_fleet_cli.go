package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"text/tabwriter"
	"time"

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
		"version and update state in a signed status message on its update topic; a node " +
		"that has not (an older build, or one whose status has not reached this node) shows " +
		"its identify version and the state unknown. SIGNAL says whether the node accepted " +
		"the newest update signal any node reports, or already runs its sequence or a later one.",
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
	fmt.Fprintln(tw, "NAME\tPEER\tVERSION\tLAST SEEN\tUPDATE\tSIGNAL")
	for _, row := range report.Rows {
		name := row.Name
		if row.Self {
			name = strings.TrimSpace(name + " (this node)")
		}
		if name == "" {
			name = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", name, peerTail(row.PeerID), orDash(row.Version),
			fleetLastSeen(row, now), fleetState(row), fleetSignal(row))
	}
	_ = tw.Flush()
	if report.LatestSignal != nil {
		fmt.Fprintf(out, "latest_signal=%s sequence=%d\n", report.LatestSignal.UpdateID, report.LatestSignal.Sequence)
	}
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
				return fmt.Sprintf("failed %s, rolled back", row.Failed.Version)
			}
			return fmt.Sprintf("failed %s, not rolled back", row.Failed.Version)
		}
		return "failed"
	case "":
		return "unknown"
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

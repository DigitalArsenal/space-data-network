package main

// `spacedatanetwork channels retention`: what a node keeps of each pull.
// The rules live in the node, so the command talks to its sync API in $DSS
// frames: GET /api/v1/sync/defaults and GET /api/v1/sync to read, POST
// /api/v1/sync with REQUESTED_ACTION SetRetention to change.

import (
	"bytes"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/spacedatanetwork/sdn-server/internal/api"
	"github.com/spacedatanetwork/sdn-server/internal/channels"
)

var retentionOrdinals = map[string]int8{
	channels.RetentionReplaceCurrent: api.DSSRetentionReplaceCurrent,
	channels.RetentionKeepAll:        api.DSSRetentionKeepAll,
	channels.RetentionArchiveAll:     api.DSSRetentionArchiveAll,
}

func retentionWordOf(ordinal int8) string {
	for word, value := range retentionOrdinals {
		if value == ordinal {
			return word
		}
	}
	return fmt.Sprintf("unknown(%d)", ordinal)
}

func newChannelsRetentionCommand() *cobra.Command {
	var apiURL string
	var insecure bool
	cmd := &cobra.Command{
		Use:   "retention [STANDARD|CHANNEL_ID RULE]",
		Short: "Show or set what this node keeps of each pull",
		Long: `Without arguments, lists each standard's default rule and every channel's rule.

  spacedatanetwork channels retention CAT replace-current
      sets a standard's default
  spacedatanetwork channels retention space-data-network-02-OMM keep-all
      sets one channel's rule (one provider's lanes of one standard)

Rules:
  replace-current  each pull replaces the previous one: a full replacement
  keep-all         every pull stays in the store; nothing is pinned
  archive-all      every pull stays and is pinned
  default          clears the choice, so the default applies

Built in: CAT is replace-current; every other standard is keep-all.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 && len(args) != 2 {
				return fmt.Errorf("give no arguments, or a standard or channel id and a rule")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			base := firstNonEmptyChannelOption(strings.TrimSpace(apiURL), strings.TrimSpace(os.Getenv("SDN_API_URL")))
			if base == "" {
				return fmt.Errorf("name the node with --api-url or SDN_API_URL")
			}
			client, err := newChannelAPIClient(base, 30*time.Second, insecure)
			if err != nil {
				return err
			}
			if len(args) == 2 {
				if err := setChannelRetention(cmd, client, base, args[0], args[1]); err != nil {
					return err
				}
			}
			return printChannelRetention(cmd, client, base)
		},
	}
	cmd.Flags().StringVar(&apiURL, "api-url", "", "SDN API base URL (default: SDN_API_URL)")
	addChannelInsecureTLSFlag(cmd, &insecure)
	return cmd
}

// setChannelRetention sends one SetRetention: a standard's default, or one
// channel's rule.
func setChannelRetention(cmd *cobra.Command, client *http.Client, base, target, rule string) error {
	var ordinal *int8
	if !strings.EqualFold(strings.TrimSpace(rule), "default") {
		word, ok := channels.NormalizeRetention(rule)
		if !ok {
			return fmt.Errorf("rule must be replace-current, keep-all, archive-all or default, not %q", rule)
		}
		value := retentionOrdinals[word]
		ordinal = &value
	}
	var frame []byte
	if parsed, err := channels.ParseChannelID(target); err == nil {
		frame = api.EncodeDSSSetRetention(parsed.StandardCode, parsed.SourceID, "", ordinal)
	} else if code, err := channels.AssertStandardCode(strings.ToUpper(strings.TrimSpace(target))); err == nil {
		frame = api.EncodeDSSSetRetention(code, "", "", ordinal)
	} else {
		return fmt.Errorf("%q is neither a standard code nor a channel id", target)
	}
	_, err := syncAPIFrames(cmd, client, http.MethodPost, base, "", frame)
	return err
}

// printChannelRetention lists the defaults, then every channel with its
// rule; a rule that differs from its standard's default is the channel's
// own choice.
func printChannelRetention(cmd *cobra.Command, client *http.Client, base string) error {
	defaultFrames, err := syncAPIFrames(cmd, client, http.MethodGet, base, "/defaults", nil)
	if err != nil {
		return err
	}
	laneFrames, err := syncAPIFrames(cmd, client, http.MethodGet, base, "", nil)
	if err != nil {
		return err
	}
	defaults := map[string]string{}
	for _, frame := range defaultFrames {
		dss, err := api.DecodeDSS(frame)
		if err != nil {
			return err
		}
		defaults[channels.RetentionStandardCode(string(dss.SCHEMA_NAME()))] = retentionWordOf(int8(dss.RETENTION()))
	}
	out := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
	fmt.Fprintln(out, "STANDARD\tDEFAULT")
	codes := make([]string, 0, len(defaults))
	for code := range defaults {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	for _, code := range codes {
		label := code
		if code == channels.NodeDefaultKey {
			label = "* (every other standard)"
		}
		fmt.Fprintf(out, "%s\t%s\n", label, defaults[code])
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "CHANNEL\tLANE\tRULE\tSET BY")
	type row struct{ channel, lane, rule, note string }
	rows := make([]row, 0, len(laneFrames))
	for _, frame := range laneFrames {
		dss, err := api.DecodeDSS(frame)
		if err != nil {
			return err
		}
		code := channels.RetentionStandardCode(string(dss.SCHEMA_NAME()))
		rule := retentionWordOf(int8(dss.RETENTION()))
		standardDefault, ok := defaults[code]
		if !ok {
			standardDefault = defaults[channels.NodeDefaultKey]
		}
		note := "standard default"
		if rule != standardDefault {
			note = "this channel"
		}
		rows = append(rows, row{
			channel: string(dss.CHANNEL_ID()),
			lane:    code + " " + string(dss.PROVIDER_ID()) + "/" + string(dss.SOURCE_NAME()),
			rule:    rule,
			note:    note,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].lane < rows[j].lane })
	for _, r := range rows {
		fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", r.channel, r.lane, r.rule, r.note)
	}
	return out.Flush()
}

// syncAPIFrames calls the node's sync API and returns the $DSS frames of a
// 2xx answer; any other answer becomes its $QRP message.
func syncAPIFrames(cmd *cobra.Command, client *http.Client, method, base, suffix string, body []byte) ([][]byte, error) {
	endpoint, err := url.Parse(strings.TrimRight(base, "/") + api.SyncPath + suffix)
	if err != nil {
		return nil, fmt.Errorf("invalid api-url: %w", err)
	}
	req, err := http.NewRequestWithContext(cmd.Context(), method, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", api.StreamContentType)
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
	}
	prepareChannelAPIRequest(cmd, req)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, endpoint.Path, err)
	}
	defer resp.Body.Close()
	frames, readErr := api.ReadFrames(resp.Body, 64<<20)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if readErr == nil && len(frames) == 1 {
			if qrp, err := api.ParseQRP(frames[0]); err == nil && len(qrp.MESSAGE()) > 0 {
				return nil, fmt.Errorf("%s %s: %s", method, endpoint.Path, qrp.MESSAGE())
			}
		}
		return nil, fmt.Errorf("%s %s: %s", method, endpoint.Path, resp.Status)
	}
	if readErr != nil {
		return nil, fmt.Errorf("read %s: %w", endpoint.Path, readErr)
	}
	return frames, nil
}

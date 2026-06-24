package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/spacedatanetwork/sdn-server/internal/directory"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

type identityDirectoryOptions struct {
	Kind       string
	Format     string
	Limit      int
	File       string
	OutputPath string
	Source     string
	EPMCID     string
}

var identityDirectoryOpts = identityDirectoryOptions{
	Format: "table",
	Kind:   "all",
	Limit:  100,
	Source: "cli-import",
}

var identityDirectoryCmd = &cobra.Command{
	Use:   "directory",
	Short: "Import, list, show, and download public EPM directory records",
}

var identityDirectoryListCmd = &cobra.Command{
	Use:   "list [query]",
	Short: "List public EPM directory records",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		query := ""
		if len(args) > 0 {
			query = args[0]
		}
		return runIdentityDirectoryList(cmd.OutOrStdout(), identityDirectoryOpts, query)
	},
}

var identityDirectoryShowCmd = &cobra.Command{
	Use:   "show <peer-id>",
	Short: "Show one public EPM directory record",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runIdentityDirectoryShow(cmd.OutOrStdout(), identityDirectoryOpts, args[0])
	},
}

var identityDirectoryImportCmd = &cobra.Command{
	Use:   "import --file <path>",
	Short: "Import a public EPM directory record from JSON or vCard",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runIdentityDirectoryImport(cmd.OutOrStdout(), identityDirectoryOpts)
	},
}

var identityDirectoryDownloadCmd = &cobra.Command{
	Use:   "download <peer-id>",
	Short: "Download a public EPM directory record as JSON or vCard",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runIdentityDirectoryDownload(cmd.OutOrStdout(), identityDirectoryOpts, args[0])
	},
}

func init() {
	addIdentityDirectorySharedFlags(identityDirectoryListCmd, &identityDirectoryOpts)
	addIdentityDirectorySharedFlags(identityDirectoryShowCmd, &identityDirectoryOpts)
	addIdentityDirectorySharedFlags(identityDirectoryImportCmd, &identityDirectoryOpts)
	addIdentityDirectorySharedFlags(identityDirectoryDownloadCmd, &identityDirectoryOpts)
	identityDirectoryImportCmd.Flags().StringVar(&identityDirectoryOpts.File, "file", "", "JSON, EPM JSON, or vCard file to import")
	identityDirectoryImportCmd.Flags().StringVar(&identityDirectoryOpts.Source, "source", "cli-import", "directory source label for imported records")
	identityDirectoryImportCmd.Flags().StringVar(&identityDirectoryOpts.EPMCID, "epm-cid", "", "EPM CID to associate with imported JSON/vCard records")
	identityDirectoryDownloadCmd.Flags().StringVarP(&identityDirectoryOpts.OutputPath, "output", "o", "", "write downloaded record to path")
	identityDirectoryCmd.AddCommand(
		identityDirectoryListCmd,
		identityDirectoryShowCmd,
		identityDirectoryImportCmd,
		identityDirectoryDownloadCmd,
	)
	identityCmd.AddCommand(identityDirectoryCmd)
}

func addIdentityDirectorySharedFlags(cmd *cobra.Command, options *identityDirectoryOptions) {
	cmd.Flags().StringVar(&options.Kind, "kind", "all", "directory kind: all, node, or user")
	cmd.Flags().StringVar(&options.Format, "format", "table", "output format: table, json, csv, or vcard for download")
	cmd.Flags().IntVar(&options.Limit, "limit", 100, "maximum records")
}

func runIdentityDirectoryList(out io.Writer, options identityDirectoryOptions, query string) error {
	format, err := normalizeSearchFormat(options.Format)
	if err != nil {
		return err
	}
	store, err := openSearchStore()
	if err != nil {
		return err
	}
	defer store.Close()

	records, err := queryIdentityDirectory(store, options.Kind, strings.TrimSpace(query), "", options.Limit)
	if err != nil {
		return err
	}
	return writeSearchResult(out, searchResult{
		Count:   len(records),
		Results: identityDirectoryRows(records),
	}, identityDirectoryFields(), format)
}

func runIdentityDirectoryShow(out io.Writer, options identityDirectoryOptions, peerID string) error {
	format, err := normalizeSearchFormat(options.Format)
	if err != nil {
		return err
	}
	store, err := openSearchStore()
	if err != nil {
		return err
	}
	defer store.Close()

	records, err := queryIdentityDirectory(store, options.Kind, "", strings.TrimSpace(peerID), 1)
	if err != nil {
		return err
	}
	if len(records) == 0 {
		return fmt.Errorf("directory record %q not found", peerID)
	}
	return writeSearchResult(out, searchResult{
		Count:   1,
		Results: identityDirectoryRows(records[:1]),
	}, identityDirectoryFields(), format)
}

func runIdentityDirectoryImport(out io.Writer, options identityDirectoryOptions) error {
	if strings.TrimSpace(options.File) == "" {
		return fmt.Errorf("--file is required")
	}
	payload, err := os.ReadFile(options.File)
	if err != nil {
		return fmt.Errorf("read directory import file: %w", err)
	}

	req, err := identityDirectoryImportRequest(payload, options)
	if err != nil {
		return err
	}
	store, err := openSearchStore()
	if err != nil {
		return err
	}
	defer store.Close()

	result, err := directory.NewService(store).ImportRecord(req)
	if err != nil {
		return err
	}
	records := append([]storage.DirectoryRecord{}, result.Nodes...)
	records = append(records, result.Users...)

	format, err := normalizeSearchFormat(options.Format)
	if err != nil {
		return err
	}
	return writeSearchResult(out, searchResult{
		Count:   len(records),
		Results: identityDirectoryRows(records),
	}, identityDirectoryFields(), format)
}

func runIdentityDirectoryDownload(out io.Writer, options identityDirectoryOptions, peerID string) error {
	format := strings.ToLower(strings.TrimSpace(options.Format))
	if format == "" || format == "table" {
		format = "json"
	}
	store, err := openSearchStore()
	if err != nil {
		return err
	}
	defer store.Close()

	records, err := queryIdentityDirectory(store, options.Kind, "", strings.TrimSpace(peerID), 1)
	if err != nil {
		return err
	}
	if len(records) == 0 {
		return fmt.Errorf("directory record %q not found", peerID)
	}

	var data []byte
	switch format {
	case "json":
		data, err = identityDirectoryRecordJSON(records[0])
	case "vcard", "vcf", "text":
		data = []byte(identityDirectoryRecordVCard(records[0]))
	default:
		return fmt.Errorf("unsupported identity directory download format %q (use json or vcard)", options.Format)
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(options.OutputPath) != "" {
		return os.WriteFile(options.OutputPath, data, 0o600)
	}
	_, err = out.Write(ensureTrailingNewline(data))
	return err
}

func identityDirectoryImportRequest(payload []byte, options identityDirectoryOptions) (directory.ImportRecordRequest, error) {
	text := strings.TrimSpace(string(payload))
	req := directory.ImportRecordRequest{
		Kind:   normalizeIdentityDirectoryKindForImport(options.Kind),
		Source: strings.TrimSpace(options.Source),
		EPMCID: strings.TrimSpace(options.EPMCID),
	}
	if strings.HasPrefix(text, "BEGIN:VCARD") || strings.EqualFold(filepath.Ext(options.File), ".vcf") || strings.EqualFold(filepath.Ext(options.File), ".vcard") {
		req.VCard = string(payload)
		return req, nil
	}

	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return directory.ImportRecordRequest{}, fmt.Errorf("decode directory import JSON: %w", err)
	}
	if value, ok := decoded["kind"].(string); ok && req.Kind == "" {
		req.Kind = value
	}
	if value, ok := decoded["source"].(string); ok && req.Source == "" {
		req.Source = value
	}
	if value, ok := decoded["epm_cid"].(string); ok && req.EPMCID == "" {
		req.EPMCID = value
	}
	if value, ok := decoded["vcard"].(string); ok && strings.TrimSpace(value) != "" {
		req.VCard = value
		return req, nil
	}
	if value, ok := decoded["epm_json"].(map[string]any); ok {
		req.EPMJSON = value
		return req, nil
	}
	if value, ok := decoded["record"].(map[string]any); ok {
		req.Record = value
		return req, nil
	}
	req.EPMJSON = decoded
	return req, nil
}

func queryIdentityDirectory(store *storage.FlatSQLStore, kind, search, peerID string, limit int) ([]storage.DirectoryRecord, error) {
	kinds, err := identityDirectoryKinds(kind)
	if err != nil {
		return nil, err
	}
	records := []storage.DirectoryRecord{}
	for _, currentKind := range kinds {
		rows, err := store.QueryDirectory(storage.DirectoryQuery{
			Kind:   currentKind,
			Search: search,
			PeerID: peerID,
			Limit:  limit,
		})
		if err != nil {
			return nil, err
		}
		records = append(records, rows...)
	}
	if limit > 0 && len(records) > limit {
		records = records[:limit]
	}
	return records, nil
}

func identityDirectoryKinds(kind string) ([]string, error) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "", "all":
		return []string{directory.KindNode, directory.KindUser}, nil
	case "node", "nodes":
		return []string{directory.KindNode}, nil
	case "user", "users", "person", "people":
		return []string{directory.KindUser}, nil
	default:
		return nil, fmt.Errorf("unsupported identity directory kind %q (use all, node, or user)", kind)
	}
}

func normalizeIdentityDirectoryKindForImport(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "", "all":
		return ""
	case "node", "nodes":
		return directory.KindNode
	case "user", "users", "person", "people":
		return directory.KindUser
	default:
		return strings.TrimSpace(kind)
	}
}

func identityDirectoryFields() []string {
	return []string{"kind", "peer_id", "dn", "legal_name", "bitcoin_address", "epm_cid", "source", "updated_at"}
}

func identityDirectoryRows(records []storage.DirectoryRecord) []map[string]any {
	rows := make([]map[string]any, 0, len(records))
	for _, record := range records {
		rows = append(rows, map[string]any{
			"kind":            record.Kind,
			"peer_id":         record.PeerID,
			"dn":              record.DN,
			"legal_name":      record.LegalName,
			"bitcoin_address": record.BitcoinAddress,
			"epm_cid":         record.EPMCID,
			"source":          record.Source,
			"updated_at":      formatSearchUnix(record.UpdatedAt),
		})
	}
	return rows
}

func identityDirectoryRecordJSON(record storage.DirectoryRecord) ([]byte, error) {
	payload := strings.TrimSpace(record.EPMJSON)
	if payload == "" {
		payload = "{}"
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		return nil, fmt.Errorf("decode stored directory EPM JSON: %w", err)
	}
	decoded["directory_kind"] = record.Kind
	decoded["peer_id"] = record.PeerID
	if record.EPMCID != "" {
		decoded["epm_cid"] = record.EPMCID
	}
	return json.MarshalIndent(decoded, "", "  ")
}

func identityDirectoryRecordVCard(record storage.DirectoryRecord) string {
	fields := []string{
		"BEGIN:VCARD",
		"VERSION:4.0",
		"FN:" + identityDirectoryVCardValue(firstNonEmptyDirectoryString(record.DN, record.PeerID)),
		"UID:" + identityDirectoryVCardValue(record.PeerID),
		"X-SDN-DIRECTORY-KIND:" + identityDirectoryVCardValue(record.Kind),
		"X-SDN-PEER-ID:" + identityDirectoryVCardValue(record.PeerID),
	}
	if record.LegalName != "" {
		fields = append(fields, "ORG:"+identityDirectoryVCardValue(record.LegalName))
	}
	if record.BitcoinAddress != "" {
		fields = append(fields, "X-SDN-BITCOIN-ADDRESS:"+identityDirectoryVCardValue(record.BitcoinAddress))
	}
	if record.EPMCID != "" {
		fields = append(fields, "X-SDN-EPM-CID:"+identityDirectoryVCardValue(record.EPMCID))
	}
	fields = append(fields, "END:VCARD")
	return strings.Join(fields, "\n") + "\n"
}

func identityDirectoryVCardValue(value string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "\n", "\\n", "\r", "", ";", "\\;", ",", "\\,")
	return replacer.Replace(strings.TrimSpace(value))
}

func firstNonEmptyDirectoryString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

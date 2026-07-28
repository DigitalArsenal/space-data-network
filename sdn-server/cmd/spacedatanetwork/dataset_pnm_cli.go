package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spf13/cobra"
)

type datasetPNMListEntry struct {
	RecordCID   string `json:"recordCid"`
	PeerID      string `json:"peerId"`
	Timestamp   string `json:"timestamp"`
	Schema      string `json:"schema"`
	FileID      string `json:"fileId"`
	ManifestCID string `json:"manifestCid"`
}

var datasetPNMsCmd = &cobra.Command{
	Use:   "dataset-pnms",
	Short: "Inspect and recover stored dataset-publication PNMs",
}

var datasetPNMsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List stored dataset-publication PNMs",
	RunE:  runDatasetPNMsList,
}

var datasetPNMsExportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export one stored dataset-publication PNM as base64",
	RunE:  runDatasetPNMsExport,
}

var datasetPNMsImportCmd = &cobra.Command{
	Use:   "import",
	Short: "Import a dataset-publication PNM into local storage",
	RunE:  runDatasetPNMsImport,
}

var (
	datasetPNMLimit          int
	datasetPNMSchema         string
	datasetPNMFileID         string
	datasetPNMFileIDContains string
	datasetPNMBase64         string
	datasetPNMBase64File     string
	datasetPNMPeerID         string
)

func init() {
	datasetPNMsListCmd.Flags().IntVar(&datasetPNMLimit, "limit", 2000, "maximum recent PNM records to inspect")
	datasetPNMsListCmd.Flags().StringVar(&datasetPNMSchema, "schema", "", "filter by dataset schema, e.g. OMM.fbs")
	datasetPNMsListCmd.Flags().StringVar(&datasetPNMFileIDContains, "file-id-contains", "", "filter PNMs whose FILE_ID contains this text")

	datasetPNMsExportCmd.Flags().IntVar(&datasetPNMLimit, "limit", 2000, "maximum recent PNM records to inspect")
	datasetPNMsExportCmd.Flags().StringVar(&datasetPNMFileID, "file-id", "", "exact PNM FILE_ID to export")
	datasetPNMsExportCmd.Flags().StringVar(&datasetPNMFileIDContains, "file-id-contains", "", "export the first PNM whose FILE_ID contains this text")

	datasetPNMsImportCmd.Flags().StringVar(&datasetPNMBase64, "base64", "", "base64 encoded size-prefixed PNM bytes")
	datasetPNMsImportCmd.Flags().StringVar(&datasetPNMBase64File, "base64-file", "", "file containing base64 encoded size-prefixed PNM bytes, or - for stdin")
	datasetPNMsImportCmd.Flags().StringVar(&datasetPNMPeerID, "peer", "", "provider peer ID to associate with the imported PNM")

	datasetPNMsCmd.AddCommand(datasetPNMsListCmd)
	datasetPNMsCmd.AddCommand(datasetPNMsExportCmd)
	datasetPNMsCmd.AddCommand(datasetPNMsImportCmd)
	rootCmd.AddCommand(datasetPNMsCmd)
}

func runDatasetPNMsList(cmd *cobra.Command, args []string) error {
	store, err := openDatasetPNMStore()
	if err != nil {
		return err
	}
	defer store.Close()
	entries, err := listDatasetPNMs(store, datasetPNMLimit, datasetPNMSchema, datasetPNMFileIDContains)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(entries)
}

func runDatasetPNMsExport(cmd *cobra.Command, args []string) error {
	if strings.TrimSpace(datasetPNMFileID) == "" && strings.TrimSpace(datasetPNMFileIDContains) == "" {
		return fmt.Errorf("--file-id or --file-id-contains is required")
	}
	store, err := openDatasetPNMStore()
	if err != nil {
		return err
	}
	defer store.Close()
	record, err := findDatasetPNM(store, datasetPNMLimit, datasetPNMFileID, datasetPNMFileIDContains)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(os.Stdout, base64.StdEncoding.EncodeToString(record.Data))
	return err
}

func runDatasetPNMsImport(cmd *cobra.Command, args []string) error {
	peerID := strings.TrimSpace(datasetPNMPeerID)
	if peerID == "" {
		return fmt.Errorf("--peer is required")
	}
	encoded := strings.TrimSpace(datasetPNMBase64)
	if datasetPNMBase64File != "" {
		data, err := readDatasetPNMBase64File(datasetPNMBase64File)
		if err != nil {
			return err
		}
		encoded = strings.TrimSpace(string(data))
	}
	if encoded == "" {
		return fmt.Errorf("--base64 or --base64-file is required")
	}
	pnmBytes, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return fmt.Errorf("decode PNM base64: %w", err)
	}
	if !PNM.SizePrefixedPNMBufferHasIdentifier(pnmBytes) {
		return fmt.Errorf("PNM buffer missing size-prefixed identifier")
	}
	store, err := openDatasetPNMStore()
	if err != nil {
		return err
	}
	defer store.Close()
	cid, err := store.Store("PNM.fbs", pnmBytes, peerID, nil)
	if err != nil {
		return fmt.Errorf("store PNM: %w", err)
	}
	pnm := PNM.GetSizePrefixedRootAsPNM(pnmBytes, 0)
	result := datasetPNMListEntry{
		RecordCID:   cid,
		PeerID:      peerID,
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		Schema:      datasetPNMFileIDSchema(string(pnm.FILE_ID())),
		FileID:      string(pnm.FILE_ID()),
		ManifestCID: string(pnm.CID()),
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

func openDatasetPNMStore() (*storage.FlatSQLStore, error) {
	cfg, _, err := config.LoadResolved(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	validator, err := sds.NewValidator(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize schema validator: %w", err)
	}
	store, err := openStoreForReading(cfg.Storage.Path, validator)
	if err != nil {
		return nil, err
	}
	return store, nil
}

func listDatasetPNMs(store *storage.FlatSQLStore, limit int, schemaFilter, fileIDContains string) ([]datasetPNMListEntry, error) {
	records, err := store.QueryRecentRecords("PNM.fbs", limit)
	if err != nil {
		return nil, fmt.Errorf("query recent PNM records: %w", err)
	}
	schemaFilter = strings.TrimSpace(schemaFilter)
	fileIDContains = strings.TrimSpace(fileIDContains)
	entries := make([]datasetPNMListEntry, 0, len(records))
	for _, record := range records {
		if record == nil || !PNM.SizePrefixedPNMBufferHasIdentifier(record.Data) {
			continue
		}
		pnm := PNM.GetSizePrefixedRootAsPNM(record.Data, 0)
		fileID := strings.TrimSpace(string(pnm.FILE_ID()))
		schema := datasetPNMFileIDSchema(fileID)
		if schemaFilter != "" && schema != schemaFilter {
			continue
		}
		if fileIDContains != "" && !strings.Contains(fileID, fileIDContains) {
			continue
		}
		entries = append(entries, datasetPNMListEntry{
			RecordCID:   record.CID,
			PeerID:      record.PeerID,
			Timestamp:   record.Timestamp.UTC().Format(time.RFC3339),
			Schema:      schema,
			FileID:      fileID,
			ManifestCID: strings.TrimSpace(string(pnm.CID())),
		})
	}
	return entries, nil
}

func findDatasetPNM(store *storage.FlatSQLStore, limit int, exactFileID, fileIDContains string) (*storage.Record, error) {
	records, err := store.QueryRecentRecords("PNM.fbs", limit)
	if err != nil {
		return nil, fmt.Errorf("query recent PNM records: %w", err)
	}
	exactFileID = strings.TrimSpace(exactFileID)
	fileIDContains = strings.TrimSpace(fileIDContains)
	for _, record := range records {
		if record == nil || !PNM.SizePrefixedPNMBufferHasIdentifier(record.Data) {
			continue
		}
		pnm := PNM.GetSizePrefixedRootAsPNM(record.Data, 0)
		fileID := strings.TrimSpace(string(pnm.FILE_ID()))
		if exactFileID != "" && fileID == exactFileID {
			return record, nil
		}
		if exactFileID == "" && fileIDContains != "" && strings.Contains(fileID, fileIDContains) {
			return record, nil
		}
	}
	return nil, fmt.Errorf("matching dataset PNM not found")
}

func datasetPNMFileIDSchema(fileID string) string {
	for _, part := range strings.Split(fileID, ":") {
		part = strings.TrimSpace(part)
		if strings.HasSuffix(part, ".fbs") {
			return part
		}
	}
	return ""
}

func readDatasetPNMBase64File(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return data, nil
}

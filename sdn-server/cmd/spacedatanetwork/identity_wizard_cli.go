package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	"github.com/libp2p/go-libp2p/core/peer"
	qrgen "github.com/skip2/go-qrcode"
	"github.com/spf13/cobra"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/directory"
	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

type identityWizardOptions struct {
	Format     string
	OutputPath string
	Sets       []string
	Yes        bool
}

var identityWizardOpts identityWizardOptions

func init() {
	identityWizardCmd.Flags().StringVar(&identityWizardOpts.Format, "format", "text", "output format: text, json, csv, flatbuffer, qrcode")
	identityWizardCmd.Flags().StringVarP(&identityWizardOpts.OutputPath, "output", "o", "", "write FlatBuffer output to path")
	identityWizardCmd.Flags().StringArrayVar(&identityWizardOpts.Sets, "set", nil, "set public identity field as key=value (repeatable)")
	identityWizardCmd.Flags().BoolVarP(&identityWizardOpts.Yes, "yes", "y", false, "accept changes without confirmation")
	identityCmd.AddCommand(identityWizardCmd)
}

var identityWizardCmd = &cobra.Command{
	Use:   "wizard",
	Short: "Create or update the node's public EPM identity profile",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runIdentityWizard(cmd.InOrStdin(), cmd.OutOrStdout(), identityWizardOpts)
	},
}

func runIdentityWizard(in io.Reader, out io.Writer, options identityWizardOptions) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	validator, err := sds.NewValidator(nil)
	if err != nil {
		return fmt.Errorf("failed to initialize schema validator: %w", err)
	}
	store, err := storage.NewFlatSQLStore(cfg.Storage.Path, validator)
	if err != nil {
		return fmt.Errorf("failed to open storage: %w", err)
	}
	defer store.Close()

	peerID, err := loadWizardPeerID(store)
	if err != nil {
		return err
	}
	return runIdentityWizardWithIO(in, out, options, store, peerID, cfg.Storage.Path)
}

func loadWizardPeerID(store *storage.FlatSQLStore) (peer.ID, error) {
	records, err := store.QueryRawRecords(storage.RawRecordQuery{
		SchemaName: "EPM.fbs",
		ProviderID: "local-node",
		SourceName: "local-epm",
		BatchID:    "local",
		Limit:      1,
	})
	if err != nil {
		return "", fmt.Errorf("load local public EPM identity: %w", err)
	}
	if len(records) == 0 || strings.TrimSpace(records[0].PeerID) == "" {
		return "", fmt.Errorf("no local public EPM identity found; run spacedatanetwork init and start the daemon once before running identity wizard")
	}
	peerID, err := peer.Decode(records[0].PeerID)
	if err != nil {
		return "", fmt.Errorf("decode local EPM peer ID %q: %w", records[0].PeerID, err)
	}
	return peerID, nil
}

func runIdentityWizardWithIO(in io.Reader, out io.Writer, options identityWizardOptions, store *storage.FlatSQLStore, peerID peer.ID, dataDir string) error {
	if in == nil {
		in = strings.NewReader("")
	}
	if out == nil {
		out = io.Discard
	}
	if store == nil {
		return errors.New("identity wizard store is required")
	}
	if peerID == "" {
		return errors.New("identity wizard peer ID is required")
	}

	service := epm.NewService(nil, peers.NewRegistry(false, nil), peerID, "", dataDir)
	service.SetProfileStore(store)
	if err := service.Init(); err != nil {
		return fmt.Errorf("initialize EPM service: %w", err)
	}

	profile := cloneEPMProfile(service.GetNodeProfile())
	if err := applyIdentityWizardSets(profile, options.Sets); err != nil {
		return err
	}
	reader := bufio.NewReader(in)
	if len(options.Sets) == 0 {
		if err := promptIdentityWizardProfile(reader, profile); err != nil {
			return err
		}
	}
	if !options.Yes {
		accepted, err := confirmIdentityWizardProfile(reader)
		if err != nil {
			return err
		}
		if !accepted {
			return errors.New("identity wizard cancelled")
		}
	}

	if err := service.UpdateProfile(profile); err != nil {
		return fmt.Errorf("update EPM profile: %w", err)
	}
	epmBytes := service.GetNodeEPM()
	epmJSON, err := epm.DirectoryRecordJSONFromEPM(epmBytes, peerID.String())
	if err != nil {
		return fmt.Errorf("build EPM directory JSON: %w", err)
	}
	epmCID, err := epm.ComputeEPMCID(epmBytes)
	if err != nil {
		return fmt.Errorf("compute EPM CID: %w", err)
	}
	if err := directory.NewService(store).UpsertNodeEPMJSON(epmJSON, epmCID, "local-node"); err != nil {
		return fmt.Errorf("index local EPM directory record: %w", err)
	}

	return writeIdentityWizardOutput(out, service, epmBytes, epmJSON, options)
}

func cloneEPMProfile(profile *epm.Profile) *epm.Profile {
	if profile == nil {
		return &epm.Profile{}
	}
	clone := *profile
	if profile.Address != nil {
		addr := *profile.Address
		clone.Address = &addr
	}
	clone.AlternateNames = append([]string(nil), profile.AlternateNames...)
	return &clone
}

func applyIdentityWizardSets(profile *epm.Profile, sets []string) error {
	for _, assignment := range sets {
		key, value, ok := strings.Cut(assignment, "=")
		if !ok {
			return fmt.Errorf("invalid identity --set %q: expected key=value", assignment)
		}
		key = normalizeIdentityWizardSetKey(key)
		value = strings.TrimSpace(value)
		switch key {
		case "dn", "display_name":
			profile.DN = value
		case "legal_name":
			profile.LegalName = value
		case "email":
			profile.Email = value
		case "telephone", "tel":
			profile.Telephone = value
		case "website", "url", "provider_id", "bitcoin_address", "ethereum_address", "solana_address", "ens", "sns", "alternate_names":
			profile.AlternateNames = mergeIdentityWizardList(profile.AlternateNames, splitIdentityWizardList(value))
		default:
			return fmt.Errorf("unsupported identity --set key %q", key)
		}
	}
	return nil
}

func normalizeIdentityWizardSetKey(key string) string {
	return strings.ToLower(strings.TrimSpace(strings.ReplaceAll(key, "-", "_")))
}

func splitIdentityWizardList(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func mergeIdentityWizardList(existing []string, values []string) []string {
	out := make([]string, 0, len(existing)+len(values))
	seen := make(map[string]struct{})
	add := func(value string) {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	for _, value := range existing {
		add(value)
	}
	for _, value := range values {
		add(value)
	}
	return out
}

func promptIdentityWizardProfile(reader *bufio.Reader, profile *epm.Profile) error {
	var err error
	if profile.DN, err = promptIdentityWizardValue(reader, "Display name / DN", profile.DN); err != nil {
		return err
	}
	if profile.LegalName, err = promptIdentityWizardValue(reader, "Legal name", profile.LegalName); err != nil {
		return err
	}
	if profile.Email, err = promptIdentityWizardValue(reader, "Email", profile.Email); err != nil {
		return err
	}
	if profile.Telephone, err = promptIdentityWizardValue(reader, "Telephone", profile.Telephone); err != nil {
		return err
	}
	aliases, err := promptIdentityWizardValue(reader, "URLs / aliases / provider IDs / chain addresses / ENS / SNS", strings.Join(profile.AlternateNames, ", "))
	if err != nil {
		return err
	}
	profile.AlternateNames = mergeIdentityWizardList(nil, splitIdentityWizardList(aliases))
	return nil
}

func promptIdentityWizardValue(reader *bufio.Reader, label, current string) (string, error) {
	if strings.TrimSpace(current) == "" {
		fmt.Fprintf(os.Stderr, "%s: ", label)
	} else {
		fmt.Fprintf(os.Stderr, "%s [%s]: ", label, current)
	}
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	value := strings.TrimSpace(line)
	if value == "" {
		return current, nil
	}
	return value, nil
}

func confirmIdentityWizardProfile(reader *bufio.Reader) (bool, error) {
	fmt.Fprint(os.Stderr, "Write public EPM identity profile? [y/N]: ")
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

func writeIdentityWizardOutput(out io.Writer, service *epm.Service, epmBytes []byte, epmJSON map[string]any, options identityWizardOptions) error {
	switch normalizeIdentityExportFormat(options.Format) {
	case "text":
		vcardStr, err := service.GetNodeVCard()
		if err != nil {
			return fmt.Errorf("build EPM vCard: %w", err)
		}
		_, err = io.WriteString(out, string(ensureTrailingNewline([]byte(vcardStr))))
		return err
	case "json":
		jsonBytes, err := json.Marshal(epmJSON)
		if err != nil {
			return fmt.Errorf("encode EPM JSON: %w", err)
		}
		return writeIndentedJSON(out, jsonBytes)
	case "csv":
		jsonBytes, err := json.Marshal(epmJSON)
		if err != nil {
			return fmt.Errorf("encode EPM JSON: %w", err)
		}
		return writeIdentityCSV(out, jsonBytes)
	case "flatbuffer":
		return writeIdentityFlatBufferOutput(out, epmBytes, options.OutputPath)
	case "qrcode":
		vcardStr, err := service.GetNodeVCard()
		if err != nil {
			return fmt.Errorf("build EPM vCard: %w", err)
		}
		qr, err := qrgen.New(vcardStr, qrgen.Medium)
		if err != nil {
			return fmt.Errorf("encode EPM vCard QR code: %w", err)
		}
		_, err = io.WriteString(out, qr.ToSmallString(false))
		return err
	default:
		return fmt.Errorf("unsupported identity wizard output format %q (use text, json, csv, flatbuffer, or qrcode)", options.Format)
	}
}

func writeIdentityFlatBufferOutput(out io.Writer, epmBytes []byte, outputPath string) error {
	if len(epmBytes) == 0 {
		return fmt.Errorf("empty EPM FlatBuffer payload")
	}
	if !EPM.SizePrefixedEPMBufferHasIdentifier(epmBytes) {
		return fmt.Errorf("identity endpoint returned invalid EPM FlatBuffer bytes")
	}
	if strings.TrimSpace(outputPath) != "" {
		return os.WriteFile(outputPath, epmBytes, 0o600)
	}
	_, err := io.Copy(out, bytes.NewReader(epmBytes))
	return err
}

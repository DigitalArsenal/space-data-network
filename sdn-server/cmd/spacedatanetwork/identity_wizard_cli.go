package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	"github.com/libp2p/go-libp2p/core/peer"
	qrgen "github.com/skip2/go-qrcode"
	"github.com/spf13/cobra"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/directory"
	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/keys"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

type identityWizardOptions struct {
	Format       string
	OutputPath   string
	Sets         []string
	Yes          bool
	PromptWriter io.Writer
}

type identityWizardNodeIdentity struct {
	Identity *wasm.DerivedIdentity
	PeerID   peer.ID
	XPub     string
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
		return runIdentityWizard(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), identityWizardOpts)
	},
}

func runIdentityWizard(ctx context.Context, in io.Reader, out io.Writer, options identityWizardOptions) error {
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

	nodeIdentity, err := loadIdentityWizardNodeIdentity(ctx, cfg)
	if err != nil {
		return err
	}
	if options.PromptWriter == nil {
		options.PromptWriter = os.Stderr
	}
	return runIdentityWizardWithIO(in, out, options, store, nodeIdentity, cfg.Storage.Path)
}

func loadIdentityWizardNodeIdentity(ctx context.Context, cfg *config.Config) (identityWizardNodeIdentity, error) {
	if cfg == nil {
		return identityWizardNodeIdentity{}, fmt.Errorf("config is required")
	}

	keyPassword := os.Getenv("SDN_KEY_PASSWORD")
	if keyPassword == "" {
		keyPassword = cfg.Security.KeyPassword
	}
	if keyPassword == "" {
		keyPassword = keys.DeriveDefaultPassword()
	}

	mnemonicPath := filepath.Join(filepath.Dir(cfg.Storage.Path), "keys", "mnemonic")
	data, err := os.ReadFile(mnemonicPath)
	if err != nil {
		return identityWizardNodeIdentity{}, fmt.Errorf("failed to read mnemonic file %s: %w; run spacedatanetwork init first", mnemonicPath, err)
	}

	var mnemonic string
	if keys.IsMnemonicEncrypted(data) {
		mnemonic, err = keys.DecryptMnemonic(data, keyPassword)
		if err != nil {
			return identityWizardNodeIdentity{}, fmt.Errorf("failed to decrypt mnemonic (wrong password?): %w", err)
		}
	} else {
		mnemonic = string(data)
	}
	mnemonic = strings.TrimSpace(mnemonic)
	if mnemonic == "" {
		return identityWizardNodeIdentity{}, fmt.Errorf("mnemonic file %s is empty", mnemonicPath)
	}

	wp, err := resolveHDWalletWasmPath()
	if err != nil {
		return identityWizardNodeIdentity{}, err
	}
	hw, err := wasm.NewHDWalletModule(ctx, wp)
	if err != nil {
		return identityWizardNodeIdentity{}, fmt.Errorf("failed to load HD wallet WASM: %w", err)
	}
	defer hw.Close(ctx)

	seed, err := hw.MnemonicToSeed(ctx, mnemonic, "")
	if err != nil {
		return identityWizardNodeIdentity{}, fmt.Errorf("failed to derive seed: %w", err)
	}
	identity, err := hw.DeriveIdentity(ctx, seed, 0)
	if err != nil {
		return identityWizardNodeIdentity{}, fmt.Errorf("failed to derive identity: %w", err)
	}
	xpub, err := hw.DeriveXPub(ctx, seed, 0)
	if err != nil {
		return identityWizardNodeIdentity{}, fmt.Errorf("failed to derive xpub: %w", err)
	}
	return identityWizardNodeIdentity{
		Identity: identity,
		PeerID:   identity.PeerID,
		XPub:     xpub,
	}, nil
}

func runIdentityWizardWithIO(in io.Reader, out io.Writer, options identityWizardOptions, store *storage.FlatSQLStore, nodeIdentity identityWizardNodeIdentity, dataDir string) error {
	if in == nil {
		in = strings.NewReader("")
	}
	if out == nil {
		out = io.Discard
	}
	if store == nil {
		return errors.New("identity wizard store is required")
	}
	peerID := nodeIdentity.PeerID
	if peerID == "" {
		return errors.New("identity wizard peer ID is required")
	}
	if options.PromptWriter == nil {
		options.PromptWriter = io.Discard
	}
	if nodeIdentity.Identity == nil && strings.TrimSpace(nodeIdentity.XPub) == "" {
		hasIdentityMaterial, err := localEPMHasPublicIdentityMaterial(store, peerID)
		if err != nil {
			return err
		}
		if hasIdentityMaterial {
			return fmt.Errorf("local EPM for %s contains public identity material; refusing to update without derived identity/xpub", peerID)
		}
	}

	service := epm.NewService(nodeIdentity.Identity, peers.NewRegistry(false, nil), peerID, nodeIdentity.XPub, dataDir)
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
		if err := promptIdentityWizardProfile(reader, options.PromptWriter, profile); err != nil {
			return err
		}
	}
	if !options.Yes {
		accepted, err := confirmIdentityWizardProfile(reader, options.PromptWriter)
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

func localEPMHasPublicIdentityMaterial(store *storage.FlatSQLStore, peerID peer.ID) (bool, error) {
	raw, err := store.LoadLocalEPM(peerID.String())
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return false, nil
		}
		return false, fmt.Errorf("load local EPM for identity preservation check: %w", err)
	}
	epmJSON, err := epm.DirectoryRecordJSONFromEPM(raw, peerID.String())
	if err != nil {
		return false, fmt.Errorf("decode local EPM for identity preservation check: %w", err)
	}
	return epmJSONHasPublicIdentityMaterial(epmJSON), nil
}

func epmJSONHasPublicIdentityMaterial(epmJSON map[string]any) bool {
	switch keys := epmJSON["keys"].(type) {
	case []map[string]any:
		return len(keys) > 0
	case []any:
		return len(keys) > 0
	default:
		return false
	}
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

func promptIdentityWizardProfile(reader *bufio.Reader, promptOut io.Writer, profile *epm.Profile) error {
	var err error
	if profile.DN, err = promptIdentityWizardValue(reader, promptOut, "Display name / DN", profile.DN); err != nil {
		return err
	}
	if profile.LegalName, err = promptIdentityWizardValue(reader, promptOut, "Legal name", profile.LegalName); err != nil {
		return err
	}
	if profile.Email, err = promptIdentityWizardValue(reader, promptOut, "Email", profile.Email); err != nil {
		return err
	}
	if profile.Telephone, err = promptIdentityWizardValue(reader, promptOut, "Telephone", profile.Telephone); err != nil {
		return err
	}
	aliases, err := promptIdentityWizardValue(reader, promptOut, "URLs / aliases / provider IDs / chain addresses / ENS / SNS", strings.Join(profile.AlternateNames, ", "))
	if err != nil {
		return err
	}
	profile.AlternateNames = mergeIdentityWizardList(nil, splitIdentityWizardList(aliases))
	return nil
}

func promptIdentityWizardValue(reader *bufio.Reader, out io.Writer, label, current string) (string, error) {
	if strings.TrimSpace(current) == "" {
		fmt.Fprintf(out, "%s: ", label)
	} else {
		fmt.Fprintf(out, "%s [%s]: ", label, current)
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

func confirmIdentityWizardProfile(reader *bufio.Reader, out io.Writer) (bool, error) {
	fmt.Fprint(out, "Write public EPM identity profile? [y/N]: ")
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

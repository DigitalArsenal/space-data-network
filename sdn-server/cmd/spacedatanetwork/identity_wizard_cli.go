package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	SessionToken string
}

type identityWizardNodeIdentity struct {
	Identity *wasm.DerivedIdentity
	PeerID   peer.ID
	XPub     string
}

type identityWizardProfileSource struct {
	Profile      *epm.Profile
	SourceEPM    []byte
	DaemonSource bool
}

var identityWizardOpts identityWizardOptions

var errIdentityWizardDaemonUnavailable = errors.New("identity wizard daemon unavailable")

func init() {
	identityWizardCmd.Flags().StringVar(&identityWizardOpts.Format, "format", "text", "output format: text, json, csv, flatbuffer, qrcode")
	identityWizardCmd.Flags().StringVarP(&identityWizardOpts.OutputPath, "output", "o", "", "write FlatBuffer output to path")
	identityWizardCmd.Flags().StringArrayVar(&identityWizardOpts.Sets, "set", nil, "set public identity field as key=value (repeatable)")
	identityWizardCmd.Flags().BoolVarP(&identityWizardOpts.Yes, "yes", "y", false, "accept changes without confirmation")
	identityWizardCmd.Flags().StringVar(&identityWizardOpts.SessionToken, "session-token", "", "SDN wallet session token for auth-enabled daemon updates (default: SDN_SESSION_TOKEN)")
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
	profileSource, err := loadIdentityWizardProfileSource(ctx, cfg, store, nodeIdentity.PeerID, cfg.Storage.Path)
	if err != nil {
		return err
	}
	return runIdentityWizardWithProfile(ctx, in, out, options, store, nodeIdentity, cfg.Storage.Path, adminURL(cfg), profileSource)
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
	if store == nil {
		return errors.New("identity wizard store is required")
	}
	if nodeIdentity.PeerID == "" {
		return errors.New("identity wizard peer ID is required")
	}
	profileSource, err := loadIdentityWizardStoredProfileSource(store, nodeIdentity.PeerID, dataDir)
	if err != nil {
		return err
	}
	return runIdentityWizardWithProfile(context.Background(), in, out, options, store, nodeIdentity, dataDir, "", profileSource)
}

func runIdentityWizardWithProfile(ctx context.Context, in io.Reader, out io.Writer, options identityWizardOptions, store *storage.FlatSQLStore, nodeIdentity identityWizardNodeIdentity, dataDir, daemonBaseURL string, profileSource identityWizardProfileSource) error {
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
	if nodeIdentity.Identity == nil || strings.TrimSpace(nodeIdentity.XPub) == "" {
		hasIdentityMaterial, err := localEPMHasPublicIdentityMaterial(store, peerID)
		if err != nil {
			return err
		}
		if hasIdentityMaterial {
			return fmt.Errorf("local EPM for %s contains public identity material; refusing to update without derived identity/xpub", peerID)
		}
	}

	profile := cloneEPMProfile(profileSource.Profile)
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

	if profileSource.DaemonSource {
		return runIdentityWizardWithDaemonProfile(ctx, out, options, strings.TrimSpace(daemonBaseURL), profile, peerID)
	}
	if err := ensureIdentityWizardCanRebuildSourceKeys(profileSource.SourceEPM, nodeIdentity); err != nil {
		return err
	}

	service := epm.NewService(nodeIdentity.Identity, peers.NewRegistry(false, nil), peerID, nodeIdentity.XPub, dataDir)
	service.SetProfileStore(store)
	if runtimeAddrs := identityWizardRuntimeAddressesFromEPM(profileSource.SourceEPM, peerID.String()); len(runtimeAddrs) > 0 {
		if err := service.SetRuntimeAddresses(runtimeAddrs); err != nil {
			return fmt.Errorf("preserve runtime EPM addresses: %w", err)
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

func loadIdentityWizardProfileSource(ctx context.Context, cfg *config.Config, store *storage.FlatSQLStore, peerID peer.ID, dataDir string) (identityWizardProfileSource, error) {
	if daemonSource, ok, err := loadIdentityWizardDaemonProfileSource(ctx, adminURL(cfg), peerID); err != nil {
		return identityWizardProfileSource{}, err
	} else if ok {
		return daemonSource, nil
	}
	return loadIdentityWizardStoredProfileSource(store, peerID, dataDir)
}

func loadIdentityWizardDaemonProfileSource(ctx context.Context, baseURL string, peerID peer.ID) (identityWizardProfileSource, bool, error) {
	raw, err := fetchIdentityWizardDaemonEndpoint(ctx, http.MethodGet, baseURL, "/api/node/epm", nil, "", "")
	if err != nil {
		if errors.Is(err, errIdentityWizardDaemonUnavailable) {
			return identityWizardProfileSource{}, false, nil
		}
		return identityWizardProfileSource{}, true, err
	}
	profile, err := epm.ProfileFromEPMBytes(raw)
	if err != nil {
		return identityWizardProfileSource{}, true, fmt.Errorf("decode daemon EPM profile: %w", err)
	}
	if peerIDFromEPM, err := epm.PeerIDFromEPM(raw); err == nil && peerIDFromEPM != "" && peerIDFromEPM != peerID.String() {
		return identityWizardProfileSource{}, true, fmt.Errorf("daemon EPM peer ID %s does not match local identity %s", peerIDFromEPM, peerID)
	}
	return identityWizardProfileSource{
		Profile:      profile,
		SourceEPM:    raw,
		DaemonSource: true,
	}, true, nil
}

func loadIdentityWizardStoredProfileSource(store *storage.FlatSQLStore, peerID peer.ID, dataDir string) (identityWizardProfileSource, error) {
	if store == nil {
		return identityWizardProfileSource{}, errors.New("identity wizard store is required")
	}
	if peerID == "" {
		return identityWizardProfileSource{}, errors.New("identity wizard peer ID is required")
	}
	raw, err := store.LoadLocalEPM(peerID.String())
	if err == nil {
		profile, err := epm.ProfileFromEPMBytes(raw)
		if err != nil {
			return identityWizardProfileSource{}, fmt.Errorf("decode local EPM profile: %w", err)
		}
		return identityWizardProfileSource{
			Profile:   profile,
			SourceEPM: raw,
		}, nil
	}
	if !strings.Contains(err.Error(), "not found") {
		return identityWizardProfileSource{}, fmt.Errorf("load local EPM profile: %w", err)
	}
	if profile, err := epm.LoadProfile(dataDir); err == nil {
		return identityWizardProfileSource{Profile: profile}, nil
	}
	return identityWizardProfileSource{Profile: defaultIdentityWizardProfile(peerID)}, nil
}

func defaultIdentityWizardProfile(peerID peer.ID) *epm.Profile {
	return &epm.Profile{DN: "SDN Node " + peerID.ShortString()}
}

func runIdentityWizardWithDaemonProfile(ctx context.Context, out io.Writer, options identityWizardOptions, baseURL string, profile *epm.Profile, peerID peer.ID) error {
	if strings.TrimSpace(baseURL) == "" {
		return errors.New("identity wizard daemon URL is required")
	}
	epmBytes, err := updateIdentityWizardDaemonProfile(ctx, baseURL, profile, options)
	if err != nil {
		return err
	}
	epmJSON, err := epm.DirectoryRecordJSONFromEPM(epmBytes, peerID.String())
	if err != nil {
		return fmt.Errorf("build daemon EPM directory JSON: %w", err)
	}
	return writeIdentityWizardDaemonOutput(ctx, out, baseURL, epmBytes, epmJSON, options)
}

func updateIdentityWizardDaemonProfile(ctx context.Context, baseURL string, profile *epm.Profile, options identityWizardOptions) ([]byte, error) {
	body, err := json.Marshal(profile)
	if err != nil {
		return nil, fmt.Errorf("encode EPM profile update: %w", err)
	}
	epmBytes, err := fetchIdentityWizardDaemonEndpoint(ctx, http.MethodPut, baseURL, "/api/node/epm", bytes.NewReader(body), "application/json", identityWizardSessionToken(options))
	if err != nil {
		return nil, err
	}
	if !EPM.SizePrefixedEPMBufferHasIdentifier(epmBytes) {
		return nil, fmt.Errorf("daemon returned invalid EPM FlatBuffer bytes")
	}
	return epmBytes, nil
}

func fetchIdentityWizardDaemonEndpoint(ctx context.Context, method, baseURL, endpoint string, body io.Reader, contentType, sessionToken string) ([]byte, error) {
	requestURL := strings.TrimRight(baseURL, "/") + endpoint
	reqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, method, requestURL, body)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(contentType) != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if sessionToken = strings.TrimSpace(sessionToken); sessionToken != "" {
		req.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: sessionToken})
		req.Header.Set("X-Requested-With", "spacedatanetwork-cli")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: connect to local SDN daemon at %s: %v", errIdentityWizardDaemonUnavailable, baseURL, err)
	}
	defer resp.Body.Close()
	respBody, readErr := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		detail := strings.TrimSpace(string(respBody))
		if detail == "" {
			detail = resp.Status
		}
		return nil, fmt.Errorf("local SDN daemon returned %s for %s %s: %s", resp.Status, method, endpoint, detail)
	}
	if readErr != nil {
		return nil, readErr
	}
	return respBody, nil
}

func writeIdentityWizardDaemonOutput(ctx context.Context, out io.Writer, baseURL string, epmBytes []byte, epmJSON map[string]any, options identityWizardOptions) error {
	switch normalizeIdentityExportFormat(options.Format) {
	case "text":
		vcardBytes, err := fetchIdentityWizardDaemonEndpoint(ctx, http.MethodGet, baseURL, "/api/node/epm/vcard", nil, "", identityWizardSessionToken(options))
		if err != nil {
			return err
		}
		_, err = out.Write(ensureTrailingNewline(vcardBytes))
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
		vcardBytes, err := fetchIdentityWizardDaemonEndpoint(ctx, http.MethodGet, baseURL, "/api/node/epm/vcard", nil, "", identityWizardSessionToken(options))
		if err != nil {
			return err
		}
		qr, err := qrgen.New(string(vcardBytes), qrgen.Medium)
		if err != nil {
			return fmt.Errorf("encode EPM vCard QR code: %w", err)
		}
		_, err = io.WriteString(out, qr.ToSmallString(false))
		return err
	default:
		return fmt.Errorf("unsupported identity wizard output format %q (use text, json, csv, flatbuffer, or qrcode)", options.Format)
	}
}

func identityWizardSessionToken(options identityWizardOptions) string {
	if value := strings.TrimSpace(options.SessionToken); value != "" {
		return value
	}
	return strings.TrimSpace(os.Getenv("SDN_SESSION_TOKEN"))
}

func ensureIdentityWizardCanRebuildSourceKeys(epmBytes []byte, nodeIdentity identityWizardNodeIdentity) error {
	if len(epmBytes) == 0 {
		return nil
	}
	epmJSON, err := epm.DirectoryRecordJSONFromEPM(epmBytes, nodeIdentity.PeerID.String())
	if err != nil {
		return fmt.Errorf("decode local EPM for key preservation check: %w", err)
	}
	keys := identityWizardEPMJSONKeys(epmJSON)
	if len(keys) == 0 {
		return nil
	}
	for _, key := range keys {
		if !identityWizardCanRebuildKey(key, nodeIdentity) {
			return fmt.Errorf("local EPM for %s contains public key material that cannot be rebuilt offline; start the daemon and pass --session-token or stop the daemon before retrying", nodeIdentity.PeerID)
		}
	}
	return nil
}

func identityWizardEPMJSONKeys(epmJSON map[string]any) []map[string]any {
	switch typed := epmJSON["keys"].(type) {
	case []map[string]any:
		return typed
	case []any:
		keys := make([]map[string]any, 0, len(typed))
		for _, raw := range typed {
			if key, ok := raw.(map[string]any); ok {
				keys = append(keys, key)
			}
		}
		return keys
	default:
		return nil
	}
}

func identityWizardCanRebuildKey(key map[string]any, nodeIdentity identityWizardNodeIdentity) bool {
	if key == nil {
		return true
	}
	keyXPub := strings.TrimSpace(identityWizardStringValue(key["xpub"]))
	keyAddress := strings.TrimSpace(identityWizardStringValue(key["key_address"]))
	if keyXPub != "" && keyXPub == strings.TrimSpace(nodeIdentity.XPub) && identityWizardLooksLikeHDKeyPath(keyAddress) {
		return true
	}
	if nodeIdentity.Identity == nil {
		return false
	}
	info := nodeIdentity.Identity.Info()
	publicKey := strings.TrimSpace(identityWizardStringValue(key["public_key"]))
	if publicKey == "" {
		return true
	}
	switch {
	case publicKey == strings.TrimSpace(info.IdentityPubKeyHex) && keyAddress == strings.TrimSpace(info.IdentityKeyPath):
		return true
	case publicKey == strings.TrimSpace(info.SigningPubKeyHex) && keyAddress == strings.TrimSpace(info.SigningKeyPath):
		return true
	case publicKey == strings.TrimSpace(info.EncryptionPubHex) && keyAddress == strings.TrimSpace(info.EncryptionKeyPath):
		return true
	default:
		return false
	}
}

func identityWizardLooksLikeHDKeyPath(value string) bool {
	value = strings.TrimSpace(value)
	return strings.HasPrefix(value, "m/44'/")
}

func identityWizardStringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return ""
	}
}

func identityWizardRuntimeAddressesFromEPM(epmBytes []byte, peerID string) []string {
	if len(epmBytes) == 0 {
		return nil
	}
	epmJSON, err := epm.DirectoryRecordJSONFromEPM(epmBytes, peerID)
	if err != nil {
		return nil
	}
	var values []any
	switch typed := epmJSON["multiformat_address"].(type) {
	case []string:
		values = make([]any, 0, len(typed))
		for _, value := range typed {
			values = append(values, value)
		}
	case []any:
		values = typed
	default:
		return nil
	}
	out := make([]string, 0, len(values))
	for _, raw := range values {
		value, ok := raw.(string)
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" || identityWizardAddressContainsPeer(value, peerID) {
			continue
		}
		out = append(out, value)
	}
	return out
}

func identityWizardAddressContainsPeer(address, peerID string) bool {
	peerID = strings.TrimSpace(peerID)
	if peerID == "" {
		return false
	}
	return strings.Contains(address, "/ipns/"+peerID) || strings.Contains(address, "/p2p/"+peerID)
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

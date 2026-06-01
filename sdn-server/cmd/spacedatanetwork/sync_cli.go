package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	cid "github.com/ipfs/go-cid"
	"github.com/mr-tron/base58"
	"github.com/spf13/cobra"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const (
	syncProviderKindProviderID      = "provider-id"
	syncProviderKindIPFSCID         = "ipfs-cid"
	syncProviderKindIPNS            = "ipns"
	syncProviderKindXPub            = "xpub"
	syncProviderKindBitcoinAddress  = "bitcoin-address"
	syncProviderKindEthereumAddress = "ethereum-address"
	syncProviderKindSolanaAddress   = "solana-address"
	syncProviderKindENSDomain       = "ens-domain"
	syncProviderKindSNSDomain       = "sns-domain"

	syncProviderMatchDirect    = "direct"
	syncProviderMatchDirectory = "directory"
	syncProviderMatchSource    = "source"
	syncProviderMatchUnmatched = "unmatched"
)

var (
	syncStatusFlagOptions syncStatusOptions
	syncWatchFlagOptions  syncStatusOptions

	ethereumAddressPattern = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)
)

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Monitor local SDN data synchronization",
	Long: `Monitor the local FlatSQL replica state recorded by SDN data sync.

The sync monitor reads the local storage ledger directly, so it works even when
the admin API requires browser authentication.`,
}

var syncStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Print current local sync status",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSyncStatusWithOptions(cmd.Context(), cmd.OutOrStdout(), syncStatusFlagOptions)
	},
}

var syncWatchCmd = &cobra.Command{
	Use:   "watch",
	Short: "Continuously print local sync status",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSyncWatchWithOptions(cmd.Context(), cmd.OutOrStdout(), syncWatchFlagOptions)
	},
}

type syncStatusOptions struct {
	Schema       string
	ProviderID   string
	SourceName   string
	BatchID      string
	QueryProfile string
	JSON         bool
	Interval     time.Duration
}

type syncStatusSnapshot struct {
	Count          int             `json:"count"`
	GeneratedAt    string          `json:"generated_at"`
	KnownProviders []string        `json:"known_providers,omitempty"`
	Results        []syncStatusRow `json:"results"`
}

type syncStatusRow struct {
	SchemaName              string `json:"schema_name"`
	ProviderIdentifier      string `json:"provider_identifier,omitempty"`
	ProviderIdentifierKind  string `json:"provider_identifier_kind,omitempty"`
	ProviderIdentifierMatch string `json:"provider_identifier_match,omitempty"`
	ProviderPeerID          string `json:"provider_peer_id,omitempty"`
	ProviderPublicKey       string `json:"provider_public_key,omitempty"`
	ProviderID              string `json:"provider_id,omitempty"`
	SourceName              string `json:"source_name,omitempty"`
	BatchID                 string `json:"batch_id,omitempty"`
	QueryProfile            string `json:"query_profile,omitempty"`
	Status                  string `json:"status"`
	LocalRows               int64  `json:"local_rows"`
	PinnedRows              int64  `json:"pinned_rows"`
	CachedBytes             int64  `json:"cached_bytes"`
	PinnedBytes             int64  `json:"pinned_bytes"`
	SnapshotID              string `json:"snapshot_id,omitempty"`
	Head                    string `json:"head,omitempty"`
	HighWaterMark           string `json:"high_water_mark,omitempty"`
	LastSyncedAt            string `json:"last_synced_at,omitempty"`
}

type syncProviderResolution struct {
	input      string
	kind       string
	providers  syncIdentifierSet
	peers      syncIdentifierSet
	publicKeys syncIdentifierSet
}

type syncIdentifierSet struct {
	exact  map[string]string
	folded map[string]string
}

func init() {
	addSyncStatusFlags(syncStatusCmd, &syncStatusFlagOptions)
	addSyncStatusFlags(syncWatchCmd, &syncWatchFlagOptions)
	syncWatchCmd.Flags().DurationVar(&syncWatchFlagOptions.Interval, "interval", 15*time.Second, "watch polling interval")

	syncCmd.AddCommand(syncStatusCmd)
	syncCmd.AddCommand(syncWatchCmd)
	rootCmd.AddCommand(syncCmd)
}

func addSyncStatusFlags(cmd *cobra.Command, options *syncStatusOptions) {
	cmd.Flags().StringVar(&options.Schema, "schema", "", "schema name or three-letter abbreviation, for example OMM or OMM.fbs")
	cmd.Flags().StringVar(&options.ProviderID, "provider-id", "", "provider identifier: provider ID, peer ID, IPFS CID, IPNS, xpub, chain address, ENS, or SNS")
	cmd.Flags().StringVar(&options.SourceName, "source-name", "", "optional provider source/feed name; omitted means all matching sources")
	cmd.Flags().StringVar(&options.BatchID, "batch-id", "", "optional source batch ID")
	cmd.Flags().StringVar(&options.QueryProfile, "query-profile", storage.DatasetPublicationQueryProfile, "sync query profile")
	cmd.Flags().BoolVar(&options.JSON, "json", false, "print machine-readable JSON")
}

func runSyncStatusWithOptions(ctx context.Context, out io.Writer, options syncStatusOptions) error {
	snapshot, err := loadSyncStatusSnapshot(ctx, options)
	if err != nil {
		return err
	}
	if options.JSON {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(snapshot)
	}
	writeSyncStatusText(out, snapshot)
	return nil
}

func runSyncWatchWithOptions(ctx context.Context, out io.Writer, options syncStatusOptions) error {
	interval := options.Interval
	if interval <= 0 {
		interval = 15 * time.Second
	}
	for {
		if err := runSyncStatusWithOptions(ctx, out, options); err != nil {
			return err
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
			fmt.Fprintln(out)
		}
	}
}

func loadSyncStatusSnapshot(ctx context.Context, options syncStatusOptions) (syncStatusSnapshot, error) {
	select {
	case <-ctx.Done():
		return syncStatusSnapshot{}, nil
	default:
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return syncStatusSnapshot{}, fmt.Errorf("failed to load config: %w", err)
	}
	validator, err := sds.NewValidator(nil)
	if err != nil {
		return syncStatusSnapshot{}, fmt.Errorf("failed to initialize schema validator: %w", err)
	}
	store, err := storage.NewFlatSQLStore(cfg.Storage.Path, validator)
	if err != nil {
		return syncStatusSnapshot{}, fmt.Errorf("failed to open storage: %w", err)
	}
	defer store.Close()

	return buildSyncStatusSnapshot(store, options)
}

func buildSyncStatusSnapshot(store *storage.FlatSQLStore, options syncStatusOptions) (syncStatusSnapshot, error) {
	schemaName, err := normalizeSyncSchemaName(options.Schema)
	if err != nil {
		return syncStatusSnapshot{}, err
	}
	queryProfile := strings.TrimSpace(options.QueryProfile)
	if queryProfile == "" {
		queryProfile = storage.DatasetPublicationQueryProfile
	}

	resolution, err := resolveSyncProviderIdentifier(store, options.ProviderID)
	if err != nil {
		return syncStatusSnapshot{}, err
	}

	stats, err := store.LocalReplicaStats(storage.LocalReplicaStatsQuery{
		SchemaName:   schemaName,
		SourceName:   strings.TrimSpace(options.SourceName),
		BatchID:      strings.TrimSpace(options.BatchID),
		QueryProfile: queryProfile,
	})
	if err != nil {
		return syncStatusSnapshot{}, err
	}

	rows := make([]syncStatusRow, 0, len(stats))
	for _, stat := range stats {
		match, ok := resolution.matchStat(stat)
		if !ok {
			continue
		}
		rows = append(rows, syncStatusRowFromStat(stat, resolution, match))
	}
	sort.Slice(rows, func(i, j int) bool {
		return syncStatusRowSortKey(rows[i]) < syncStatusRowSortKey(rows[j])
	})

	snapshot := syncStatusSnapshot{
		Count:       len(rows),
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Results:     rows,
	}
	if len(rows) == 0 {
		snapshot.KnownProviders = knownSyncProviders(stats)
	}
	return snapshot, nil
}

func normalizeSyncSchemaName(input string) (string, error) {
	value := strings.TrimSpace(input)
	if value == "" {
		return "", nil
	}
	if strings.HasSuffix(strings.ToLower(value), ".fbs") {
		value = value[:len(value)-4]
	}
	value = strings.ToUpper(value)
	if value == "" {
		return "", fmt.Errorf("schema is required")
	}
	schemaName := value + ".fbs"

	validator, err := sds.NewValidator(nil)
	if err != nil {
		return "", fmt.Errorf("failed to initialize schema validator: %w", err)
	}
	if !validator.HasSchema(schemaName) {
		return "", fmt.Errorf("unknown schema %q", input)
	}
	return schemaName, nil
}

func classifySyncProviderIdentifier(input string) string {
	value := strings.TrimSpace(input)
	if value == "" {
		return ""
	}
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "ipns://") || strings.HasPrefix(lower, "/ipns/") {
		return syncProviderKindIPNS
	}
	if _, err := cid.Decode(stripIPFSCIDPrefix(value)); err == nil {
		return syncProviderKindIPFSCID
	}
	if strings.HasPrefix(lower, "xpub") || strings.HasPrefix(lower, "ypub") || strings.HasPrefix(lower, "zpub") {
		return syncProviderKindXPub
	}
	if ethereumAddressPattern.MatchString(value) {
		return syncProviderKindEthereumAddress
	}
	if strings.HasSuffix(lower, ".eth") {
		return syncProviderKindENSDomain
	}
	if strings.HasSuffix(lower, ".sol") {
		return syncProviderKindSNSDomain
	}
	if looksLikeBitcoinAddress(value) {
		return syncProviderKindBitcoinAddress
	}
	if looksLikeSolanaAddress(value) {
		return syncProviderKindSolanaAddress
	}
	return syncProviderKindProviderID
}

func resolveSyncProviderIdentifier(store *storage.FlatSQLStore, input string) (syncProviderResolution, error) {
	trimmed := strings.TrimSpace(input)
	resolution := syncProviderResolution{
		input:      trimmed,
		kind:       classifySyncProviderIdentifier(trimmed),
		providers:  newSyncIdentifierSet(),
		peers:      newSyncIdentifierSet(),
		publicKeys: newSyncIdentifierSet(),
	}
	if trimmed == "" {
		return resolution, nil
	}

	directValue := stripProviderIdentifierPrefix(trimmed)
	resolution.providers.add(directValue, syncProviderMatchDirect)
	resolution.peers.add(directValue, syncProviderMatchDirect)
	resolution.publicKeys.add(directValue, syncProviderMatchDirect)

	for _, search := range syncProviderDirectorySearches(trimmed, directValue) {
		records, err := store.QueryDirectory(storage.DirectoryQuery{
			Kind:   "node",
			Search: search,
			Limit:  50,
		})
		if err != nil {
			return resolution, fmt.Errorf("query provider directory: %w", err)
		}
		for _, record := range records {
			addDirectoryRecordToSyncResolution(&resolution, record)
		}
	}
	return resolution, nil
}

func syncProviderDirectorySearches(values ...string) []string {
	seen := map[string]bool{}
	searches := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		searches = append(searches, value)
	}
	return searches
}

func isDomainSyncProviderKind(kind string) bool {
	return kind == syncProviderKindENSDomain || kind == syncProviderKindSNSDomain
}

func syncProviderDomainLabel(input string) string {
	lower := strings.ToLower(strings.TrimSpace(input))
	for _, suffix := range []string{".eth", ".sol"} {
		if strings.HasSuffix(lower, suffix) {
			return strings.TrimSuffix(lower, suffix)
		}
	}
	return ""
}

func newSyncIdentifierSet() syncIdentifierSet {
	return syncIdentifierSet{
		exact:  map[string]string{},
		folded: map[string]string{},
	}
}

func (set syncIdentifierSet) add(value, match string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	if _, ok := set.exact[value]; !ok {
		set.exact[value] = match
	}
	folded := strings.ToLower(value)
	if _, ok := set.folded[folded]; !ok {
		set.folded[folded] = match
	}
}

func (set syncIdentifierSet) match(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	if match, ok := set.exact[value]; ok {
		return match, true
	}
	if match, ok := set.folded[strings.ToLower(value)]; ok {
		return match, true
	}
	return "", false
}

func (resolution syncProviderResolution) matchStat(stat storage.LocalReplicaStats) (string, bool) {
	if resolution.input == "" {
		return "", true
	}
	bestMatch := ""
	if resolution.domainMatchesStat(stat) {
		bestMatch = betterSyncProviderMatch(bestMatch, syncProviderMatchSource)
	}
	for _, candidate := range []struct {
		value string
		set   syncIdentifierSet
	}{
		{stat.ProviderID, resolution.providers},
		{stat.ProviderPeerID, resolution.peers},
		{stat.ProviderPublicKey, resolution.publicKeys},
	} {
		if match, ok := candidate.set.match(candidate.value); ok {
			bestMatch = betterSyncProviderMatch(bestMatch, match)
		}
	}
	if bestMatch != "" {
		return bestMatch, true
	}
	return syncProviderMatchUnmatched, false
}

func (resolution syncProviderResolution) domainMatchesStat(stat storage.LocalReplicaStats) bool {
	if !isDomainSyncProviderKind(resolution.kind) {
		return false
	}
	label := syncProviderDomainLabel(resolution.input)
	if label == "" {
		return false
	}
	for _, value := range []string{stat.ProviderID, stat.SourceName} {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == label || strings.HasPrefix(value, label+"-") || strings.HasPrefix(value, label+"_") {
			return true
		}
	}
	return false
}

func betterSyncProviderMatch(current, candidate string) string {
	if syncProviderMatchRank(candidate) > syncProviderMatchRank(current) {
		return candidate
	}
	return current
}

func syncProviderMatchRank(match string) int {
	switch match {
	case syncProviderMatchDirect:
		return 3
	case syncProviderMatchDirectory:
		return 2
	case syncProviderMatchSource:
		return 1
	default:
		return 0
	}
}

func addDirectoryRecordToSyncResolution(resolution *syncProviderResolution, record storage.DirectoryRecord) {
	resolution.peers.add(record.PeerID, syncProviderMatchDirectory)
	if strings.TrimSpace(record.EPMJSON) == "" {
		return
	}
	var payload any
	if err := json.Unmarshal([]byte(record.EPMJSON), &payload); err != nil {
		return
	}
	visitJSONStringFields(payload, "", func(key, value string) {
		switch strings.ToLower(key) {
		case "peer_id", "peerid", "provider_peer_id", "source_peer_id":
			resolution.peers.add(value, syncProviderMatchDirectory)
		case "provider_id":
			resolution.providers.add(value, syncProviderMatchDirectory)
		case "provider_public_key", "source_public_key", "public_key", "publickey", "signing_public_key", "signingpubkey":
			resolution.publicKeys.add(value, syncProviderMatchDirectory)
		}
	})
}

func visitJSONStringFields(value any, key string, visit func(key, value string)) {
	switch typed := value.(type) {
	case map[string]any:
		for childKey, childValue := range typed {
			visitJSONStringFields(childValue, childKey, visit)
		}
	case []any:
		for _, childValue := range typed {
			visitJSONStringFields(childValue, key, visit)
		}
	case string:
		visit(key, typed)
	}
}

func syncStatusRowFromStat(stat storage.LocalReplicaStats, resolution syncProviderResolution, match string) syncStatusRow {
	row := syncStatusRow{
		SchemaName:              stat.SchemaName,
		ProviderIdentifier:      resolution.input,
		ProviderIdentifierKind:  resolution.kind,
		ProviderIdentifierMatch: match,
		ProviderPeerID:          stat.ProviderPeerID,
		ProviderPublicKey:       stat.ProviderPublicKey,
		ProviderID:              stat.ProviderID,
		SourceName:              stat.SourceName,
		BatchID:                 stat.BatchID,
		QueryProfile:            stat.QueryProfile,
		Status:                  syncReplicaState(stat),
		LocalRows:               stat.LocalRows,
		PinnedRows:              stat.PinnedRows,
		CachedBytes:             stat.CachedBytes,
		PinnedBytes:             stat.PinnedBytes,
		SnapshotID:              stat.SnapshotID,
		Head:                    stat.Head,
		HighWaterMark:           stat.HighWaterMark,
	}
	if stat.LastSyncedAt.IsZero() {
		row.LastSyncedAt = ""
	} else {
		row.LastSyncedAt = stat.LastSyncedAt.UTC().Format(time.RFC3339)
	}
	return row
}

func syncReplicaState(stat storage.LocalReplicaStats) string {
	if stat.PinnedRows == 0 && stat.LocalRows == 0 {
		return "not_synced"
	}
	if stat.PinnedRows == 0 {
		return "local_only"
	}
	if stat.LocalRows >= stat.PinnedRows {
		return "synced"
	}
	if stat.LocalRows > 0 {
		return "partial"
	}
	return "pending"
}

func writeSyncStatusText(out io.Writer, snapshot syncStatusSnapshot) {
	fmt.Fprintf(out, "count=%d\n", snapshot.Count)
	if len(snapshot.Results) == 0 {
		if len(snapshot.KnownProviders) > 0 {
			fmt.Fprintf(out, "known_providers=%s\n", strings.Join(snapshot.KnownProviders, ","))
		}
		return
	}
	for index, row := range snapshot.Results {
		if index > 0 {
			fmt.Fprintln(out)
		}
		writeSyncStatusField(out, "schema", row.SchemaName)
		writeSyncStatusField(out, "provider_identifier", row.ProviderIdentifier)
		writeSyncStatusField(out, "provider_identifier_kind", row.ProviderIdentifierKind)
		writeSyncStatusField(out, "provider_identifier_match", row.ProviderIdentifierMatch)
		writeSyncStatusField(out, "provider_peer_id", row.ProviderPeerID)
		writeSyncStatusField(out, "provider_public_key", row.ProviderPublicKey)
		writeSyncStatusField(out, "provider_id", row.ProviderID)
		writeSyncStatusField(out, "source_name", row.SourceName)
		writeSyncStatusField(out, "batch_id", row.BatchID)
		writeSyncStatusField(out, "query_profile", row.QueryProfile)
		writeSyncStatusField(out, "status", row.Status)
		fmt.Fprintf(out, "local_rows=%d\n", row.LocalRows)
		fmt.Fprintf(out, "pinned_rows=%d\n", row.PinnedRows)
		fmt.Fprintf(out, "cached_bytes=%d\n", row.CachedBytes)
		fmt.Fprintf(out, "pinned_bytes=%d\n", row.PinnedBytes)
		writeSyncStatusField(out, "snapshot_id", row.SnapshotID)
		writeSyncStatusField(out, "head", row.Head)
		writeSyncStatusField(out, "high_water_mark", row.HighWaterMark)
		writeSyncStatusField(out, "last_synced_at", row.LastSyncedAt)
	}
}

func writeSyncStatusField(out io.Writer, key, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	fmt.Fprintf(out, "%s=%s\n", key, value)
}

func knownSyncProviders(stats []storage.LocalReplicaStats) []string {
	seen := map[string]bool{}
	for _, stat := range stats {
		if strings.TrimSpace(stat.ProviderID) != "" {
			seen[stat.ProviderID] = true
		}
		if strings.TrimSpace(stat.ProviderPeerID) != "" {
			seen[stat.ProviderPeerID] = true
		}
	}
	providers := make([]string, 0, len(seen))
	for provider := range seen {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	return providers
}

func syncStatusRowSortKey(row syncStatusRow) string {
	return strings.Join([]string{
		row.SchemaName,
		row.ProviderID,
		row.ProviderPeerID,
		row.SourceName,
		row.BatchID,
		row.QueryProfile,
	}, "\x00")
}

func stripProviderIdentifierPrefix(value string) string {
	value = strings.TrimSpace(value)
	if stripped := stripIPFSCIDPrefix(value); stripped != value {
		return stripped
	}
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "ipns://") {
		return value[len("ipns://"):]
	}
	if strings.HasPrefix(lower, "/ipns/") {
		return value[len("/ipns/"):]
	}
	return value
}

func stripIPFSCIDPrefix(value string) string {
	value = strings.TrimSpace(value)
	lower := strings.ToLower(value)
	switch {
	case strings.HasPrefix(lower, "ipfs://"):
		return value[len("ipfs://"):]
	case strings.HasPrefix(lower, "/ipfs/"):
		return value[len("/ipfs/"):]
	default:
		return value
	}
}

func looksLikeBitcoinAddress(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	if len(lower) < 26 || len(lower) > 90 {
		return false
	}
	return strings.HasPrefix(lower, "bc1") ||
		strings.HasPrefix(lower, "tb1") ||
		strings.HasPrefix(lower, "1") ||
		strings.HasPrefix(lower, "3")
}

func looksLikeSolanaAddress(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 32 || len(value) > 44 {
		return false
	}
	decoded, err := base58.Decode(value)
	return err == nil && len(decoded) == 32
}

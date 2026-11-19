package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/api"
)

// daemonSyncStatusSnapshot is `sync status` read through the running
// daemon's $DSS lane (GET /api/v1/sync, anonymous) instead of the store,
// which the daemon holds. ok is false when no daemon answers on the
// configured admin address (the caller then opens the store).
func daemonSyncStatusSnapshot(ctx context.Context, options syncStatusOptions) (syncStatusSnapshot, bool, error) {
	c, err := newAnonymousAdminClient()
	if err != nil {
		return syncStatusSnapshot{}, false, nil
	}
	probe := *c.http
	probe.Timeout = 3 * time.Second
	req, err := c.newRequest(ctx, http.MethodGet, daemonReadyPath, nil)
	if err != nil {
		return syncStatusSnapshot{}, false, nil
	}
	resp, err := probe.Do(req)
	if err != nil {
		return syncStatusSnapshot{}, false, nil
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return syncStatusSnapshot{}, false, nil
	}
	if strings.TrimSpace(options.BatchID) != "" {
		return syncStatusSnapshot{}, true, errors.New("--batch-id reads the store, which the running daemon holds: stop it, or leave --batch-id out")
	}
	schema, err := normalizeSyncSchemaName(options.Schema)
	if err != nil {
		return syncStatusSnapshot{}, true, err
	}
	q := url.Values{}
	if schema != "" {
		q.Set("schema", schema)
	}
	if v := strings.TrimSpace(options.ProviderID); v != "" {
		q.Set("provider_id", stripProviderIdentifierPrefix(v))
	}
	if v := strings.TrimSpace(options.SourceName); v != "" {
		q.Set("source", v)
	}
	req, err = c.newRequest(ctx, http.MethodGet, api.SyncPath+"?"+q.Encode(), nil)
	if err != nil {
		return syncStatusSnapshot{}, true, err
	}
	req.Header.Set("Accept", publicationStreamMediaType)
	resp, err = c.http.Do(req)
	if err != nil {
		return syncStatusSnapshot{}, true, fmt.Errorf("read the daemon's sync lane: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return syncStatusSnapshot{}, true, errors.New(refusalMessage(resp.Status, payload))
	}
	frames, err := api.ReadFrames(resp.Body, 256<<20)
	if err != nil {
		return syncStatusSnapshot{}, true, fmt.Errorf("read the daemon's sync lane: %w", err)
	}
	states := map[int8]string{api.DSSStateIdle: "idle", api.DSSStateSyncing: "syncing", api.DSSStateSynced: "synced", api.DSSStateCapped: "capped", api.DSSStateError: "error"}
	rows := make([]syncStatusRow, 0, len(frames))
	for _, frame := range frames {
		d, err := api.DecodeDSS(frame)
		if err != nil {
			return syncStatusSnapshot{}, true, err
		}
		row := syncStatusRow{
			SchemaName: string(d.SCHEMA_NAME()), ProviderID: string(d.PROVIDER_ID()), SourceName: string(d.SOURCE_NAME()),
			ProviderPeerID: string(d.PROVIDER_PEER_ID()), ProviderPublicKey: string(d.PROVIDER_PUBLIC_KEY()),
			QueryProfile: string(d.QUERY_PROFILE()), Status: states[int8(d.STATUS())],
			LocalRows: int64(min(d.LOCAL_ROWS(), uint64(1<<62))), PinnedRows: int64(min(d.PINNED_ROWS(), uint64(1<<62))),
			CachedBytes: int64(min(d.CACHED_BYTES(), uint64(1<<62))), PinnedBytes: int64(min(d.PINNED_BYTES(), uint64(1<<62))),
			SnapshotID: string(d.SNAPSHOT_ID()), Head: string(d.HEAD()), HighWaterMark: string(d.HIGH_WATER_MARK()), LastSyncedAt: string(d.LAST_SYNCED_AT()),
		}
		if row.SchemaName != "" && !strings.Contains(row.SchemaName, ".") {
			row.SchemaName += ".fbs"
		}
		if options.ProviderID != "" {
			row.ProviderIdentifier, row.ProviderIdentifierKind, row.ProviderIdentifierMatch = options.ProviderID, classifySyncProviderIdentifier(options.ProviderID), syncProviderMatchDirect
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return syncStatusRowSortKey(rows[i]) < syncStatusRowSortKey(rows[j]) })
	return syncStatusSnapshot{Count: len(rows), GeneratedAt: time.Now().UTC().Format(time.RFC3339), Results: rows}, true, nil
}

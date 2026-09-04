package capabilities

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spacedatanetwork/sdn-server/internal/flowrt"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const storagePluginID = "io.spacedatanetwork.flatsql"

// NewStorageHandlers returns flow handlers for FlatSQL database operations.
//
// Supported methods:
//   - query:  Query records by schema, day, entity_id, norad_cat_id
//   - store:  Store a FlatBuffer record
//   - delete: Delete a record by CID
func NewStorageHandlers(store *storage.FlatSQLStore) flowrt.HandlerMap {
	return NewStorageHandlersWithProducer(store, "")
}

// NewStorageHandlersWithProducer attributes flow-authored writes to the given
// producer identity (typically the node peer id) for (producer, standard)
// table routing.
func NewStorageHandlersWithProducer(store *storage.FlatSQLStore, producerID string) flowrt.HandlerMap {
	s := &storageAdapter{store: store, producerID: producerID}
	return flowrt.HandlerMap{
		storagePluginID + ":query":  s.query,
		storagePluginID + ":store":  s.storeRecord,
		storagePluginID + ":delete": s.deleteRecord,
	}
}

type storageAdapter struct {
	store      *storage.FlatSQLStore
	producerID string
}

// query executes a FlatSQL query.
// Input frame JSON: {"schema": "OMM", "day": "2026-04-07", "entity_id": "...", "norad_cat_id": "...", "limit": 100}
func (s *storageAdapter) query(ctx context.Context, args *flowrt.InvocationArgs) (*flowrt.InvocationResult, error) {
	schema := getFrameArg(args, "schema")
	if schema == "" {
		return errorResult(-1, "missing schema"), nil
	}

	day := getFrameArg(args, "day")
	entityID := getFrameArg(args, "entity_id")

	// Parse optional norad_cat_id
	var noradCatID *uint32
	if ncid := getFrameArg(args, "norad_cat_id"); ncid != "" {
		var v uint32
		if _, err := fmt.Sscanf(ncid, "%d", &v); err == nil {
			noradCatID = &v
		}
	}

	records, err := s.store.QueryByIndexedFields(schema, day, noradCatID, entityID, 100)
	if err != nil {
		return errorResult(-1, "query failed: "+err.Error()), nil
	}

	result, _ := json.Marshal(records)
	return jsonOutput(result), nil
}

// storeRecord stores a FlatBuffer record.
// Input: first frame = raw FlatBuffer bytes, JSON arg "schema" = schema name
func (s *storageAdapter) storeRecord(ctx context.Context, args *flowrt.InvocationArgs) (*flowrt.InvocationResult, error) {
	schema := getFrameArg(args, "schema")
	if schema == "" {
		return errorResult(-1, "missing schema"), nil
	}

	if len(args.Frames) == 0 || len(args.Frames[0].Bytes) == 0 {
		return errorResult(-1, "missing record data"), nil
	}

	cid, err := s.store.Store(schema, args.Frames[0].Bytes, s.producerID, nil)
	if err != nil {
		return errorResult(-1, "store failed: "+err.Error()), nil
	}

	resp, _ := json.Marshal(map[string]string{"cid": cid})
	return jsonOutput(resp), nil
}

// deleteRecord deletes a record by CID.
// Input: {"cid": "..."}
func (s *storageAdapter) deleteRecord(ctx context.Context, args *flowrt.InvocationArgs) (*flowrt.InvocationResult, error) {
	cid := getFrameArg(args, "cid")
	if cid == "" {
		return errorResult(-1, "missing cid"), nil
	}

	// Delete requires schema name; use a generic approach
	err := s.store.Delete(getFrameArg(args, "schema"), cid)
	if err != nil {
		return errorResult(-1, "delete failed: "+err.Error()), nil
	}

	resp, _ := json.Marshal(map[string]string{"status": "deleted", "cid": cid})
	return jsonOutput(resp), nil
}

package syncwire

// flatsql-sync 2.0.0: one libp2p protocol ID per op. Multistream-select is
// the op selector ($QRP has no op field); the request is exactly one
// $QRP{KIND=Request} frame and every reply begins with a $QRP Page or Error
// frame. 1.0.0 is registered nowhere, so a 1.0.0 dial fails negotiation.
const ProtocolPrefix = "/space-data-network/flatsql-sync/2.0.0/"

// The ops.
const (
	OpReadChunk      = "read-chunk"
	OpOpenManifest   = "open-manifest"
	OpListPublished  = "list-published"
	OpReadShard      = "read-shard"
	OpReadIndex      = "read-index"
	OpReadShardBatch = "read-shard-batch"
	OpWireSpeedProbe = "wire-speed-probe"
	OpListDatastores = "list-datastores"
)

// Ops lists every op, in registration order.
var Ops = []string{OpReadChunk, OpOpenManifest, OpListPublished, OpReadShard, OpReadIndex, OpReadShardBatch, OpWireSpeedProbe, OpListDatastores}

// ProtocolID is the libp2p protocol ID of op.
func ProtocolID(op string) string { return ProtocolPrefix + op }

// FILTERS fields a request may carry (Eq unless noted).
const (
	FilterQueryProfile = "QUERY_PROFILE"
	FilterDatastoreKey = "DATASTORE_KEY"
	// FilterShardCID is In: comma-separated shard CIDs (read-shard-batch).
	FilterShardCID = "SHARD_CID"
)

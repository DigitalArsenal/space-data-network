package caps

import (
	"encoding/binary"
	"encoding/json"
	"testing"

	flatbuffers "github.com/google/flatbuffers/go"

	EPM "github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	PNM "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
)

const (
	testSelfID      = "16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45"
	testCelestrakID = "16Uiu2HAm9oK2jAeVC2RMESFcYfq7BKGp2K2CCDxzoKhB5s9vpbj3"
)

// buildTestEPM returns a size-prefixed $EPM buffer.
func buildTestEPM(t *testing.T, dn string) []byte {
	t.Helper()
	b := flatbuffers.NewBuilder(256)
	dnOff := b.CreateString(dn)
	EPM.EPMStart(b)
	EPM.EPMAddDN(b, dnOff)
	EPM.EPMAddENTITY_TYPE(b, EPM.EntityTypeNode)
	b.FinishSizePrefixedWithFileIdentifier(EPM.EPMEnd(b), []byte(EPM.EPMIdentifier))
	return b.FinishedBytes()
}

// buildTestPNM returns a size-prefixed $PNM buffer with the given FILE_ID.
func buildTestPNM(t *testing.T, fileID, cid, ts string) []byte {
	t.Helper()
	b := flatbuffers.NewBuilder(256)
	fileIDOff := b.CreateString(fileID)
	cidOff := b.CreateString(cid)
	tsOff := b.CreateString(ts)
	PNM.PNMStart(b)
	PNM.PNMAddFILE_ID(b, fileIDOff)
	PNM.PNMAddCID(b, cidOff)
	PNM.PNMAddPUBLISH_TIMESTAMP(b, tsOff)
	b.FinishSizePrefixedWithFileIdentifier(PNM.PNMEnd(b), []byte(PNM.PNMIdentifier))
	return b.FinishedBytes()
}

// decodePreEncodedEnvelope splits a PreEncodedEnvelope handler response into
// the meta JSON and its binary segments (mirrors the guest-side parser).
func decodePreEncodedEnvelope(t *testing.T, response []byte) (map[string]interface{}, [][]byte) {
	t.Helper()
	magic := []byte{0x00, 'S', 'D', 'N', 'E', 'N', 'V', '1'}
	if len(response) < len(magic) || string(response[:len(magic)]) != string(magic) {
		t.Fatalf("response is not a pre-encoded envelope: %q", response[:min(len(response), 16)])
	}
	env := response[len(magic):]
	metaLen := binary.LittleEndian.Uint32(env)
	var meta map[string]interface{}
	if err := json.Unmarshal(env[4:4+metaLen], &meta); err != nil {
		t.Fatalf("meta unmarshal: %v", err)
	}
	off := 4 + int(metaLen)
	segCount := binary.LittleEndian.Uint32(env[off:])
	off += 4
	segments := make([][]byte, 0, segCount)
	for i := uint32(0); i < segCount; i++ {
		segLen := binary.LittleEndian.Uint32(env[off:])
		off += 4
		segments = append(segments, env[off:off+int(segLen)])
		off += int(segLen)
	}
	return meta, segments
}

func resultOf(t *testing.T, meta map[string]interface{}) map[string]interface{} {
	t.Helper()
	if ok, _ := meta["ok"].(bool); !ok {
		t.Fatalf("handler reported failure: %v", meta)
	}
	result, ok := meta["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing result object: %v", meta)
	}
	return result
}

func testOptions(t *testing.T) P2PCapOptions {
	celestrakEPM := buildTestEPM(t, "celestrak")
	selfEPM := buildTestEPM(t, "self-node")
	pnms := []P2PPNMRecord{
		// Newest first: OMM (new), OMM (old, superseded), CAT, a record-level
		// PNM without a dataset FILE_ID (skipped), and a malformed frame.
		{PeerID: testCelestrakID, Data: buildTestPNM(t, "celestrak:gp:OMM.fbs:2026-07-06T03:00:00Z", "bafy-omm-new", "2026-07-06T03:00:00Z")},
		{PeerID: testCelestrakID, Data: buildTestPNM(t, "celestrak:gp:OMM.fbs:2026-07-05T03:00:00Z", "bafy-omm-old", "2026-07-05T03:00:00Z")},
		{PeerID: testCelestrakID, Data: buildTestPNM(t, "celestrak:satcat:CAT.fbs:2026-07-06T02:00:00Z", "bafy-cat", "2026-07-06T02:00:00Z")},
		{PeerID: testCelestrakID, Data: buildTestPNM(t, "not-a-dataset-id", "bafy-x", "2026-07-06T01:00:00Z")},
		{PeerID: testCelestrakID, Data: []byte{1, 2, 3}},
	}
	return P2PCapOptions{
		SelfID:           testSelfID,
		SelfAgentVersion: "spacedatanetwork/test",
		SelfAddrs:        func() []string { return []string{"/ip4/10.0.0.1/tcp/4001"} },
		SelfEPM:          func() []byte { return selfEPM },
		Peers: func() []P2PPeerInfo {
			return []P2PPeerInfo{
				{ID: testCelestrakID, Addrs: []string{"/ip4/167.172.219.213/tcp/4001"}, Connected: true, AgentVersion: "spacedatanetwork/1.0.4"},
				{ID: "16Uiu2HAmZZunconnected", Connected: false},
			}
		},
		PeerEPM: func(peerID string) []byte {
			if peerID == testCelestrakID {
				return celestrakEPM
			}
			return nil
		},
		RecentPNMs: func(limit int) []P2PPNMRecord {
			if limit < len(pnms) {
				return pnms[:limit]
			}
			return pnms
		},
	}
}

func invoke(t *testing.T, opts P2PCapOptions, op string, payload string) (map[string]interface{}, [][]byte) {
	t.Helper()
	factory := NewP2PCapFactory(opts)
	handler := factory(&modulert.Module{})
	response, err := handler(op, []byte(payload))
	if err != nil {
		t.Fatalf("%s: %v", op, err)
	}
	return decodePreEncodedEnvelope(t, response)
}

func TestP2PPeersSnapshot(t *testing.T) {
	meta, segments := invoke(t, testOptions(t), "p2p.peers_snapshot", "{}")
	result := resultOf(t, meta)

	if result["self"] != testSelfID {
		t.Fatalf("self = %v", result["self"])
	}
	peers := result["peers"].([]interface{})
	if len(peers) != 3 {
		t.Fatalf("expected 3 peers (self + 2), got %d", len(peers))
	}

	// Self is listed first with its own EPM (frame 0).
	self := peers[0].(map[string]interface{})
	if self["peer_id"] != testSelfID || self["self"] != true {
		t.Fatalf("self entry wrong: %v", self)
	}
	if self["epm_index"].(float64) != 0 {
		t.Fatalf("self epm_index = %v", self["epm_index"])
	}

	// Celestrak: connected, with standards from PNMs and EPM frame 1.
	celestrak := peers[1].(map[string]interface{})
	if celestrak["peer_id"] != testCelestrakID {
		t.Fatalf("peer order: %v", celestrak)
	}
	if celestrak["connected"] != true {
		t.Fatalf("celestrak connected = %v", celestrak["connected"])
	}
	standards := celestrak["standards"].([]interface{})
	if len(standards) != 2 || standards[0] != "CAT" || standards[1] != "OMM" {
		t.Fatalf("standards = %v", standards)
	}
	if celestrak["epm_index"].(float64) != 1 {
		t.Fatalf("celestrak epm_index = %v", celestrak["epm_index"])
	}

	// The unconnected peer has no EPM.
	third := peers[2].(map[string]interface{})
	if third["epm_index"].(float64) != -1 {
		t.Fatalf("third epm_index = %v", third["epm_index"])
	}

	// Stream = 2 size-prefixed $EPM frames (self + celestrak), verbatim.
	if len(segments) != 1 {
		t.Fatalf("segments = %d", len(segments))
	}
	stream := segments[0]
	frameCount := 0
	for off := 0; off+4 <= len(stream); {
		n := int(binary.LittleEndian.Uint32(stream[off:]))
		off += 4
		if n == 0 {
			continue
		}
		frame := stream[off : off+n]
		if !EPM.SizePrefixedEPMBufferHasIdentifier(append(stream[off-4:off:off], frame...)) {
			t.Fatalf("frame %d is not a $EPM size-prefixed buffer", frameCount)
		}
		off += n
		frameCount++
	}
	if frameCount != 2 {
		t.Fatalf("epm frames = %d", frameCount)
	}
}

func TestP2PPeersSnapshotFilter(t *testing.T) {
	meta, segments := invoke(t, testOptions(t), "p2p.peers_snapshot",
		`{"peer_id":"`+testCelestrakID+`"}`)
	result := resultOf(t, meta)
	peers := result["peers"].([]interface{})
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(peers))
	}
	entry := peers[0].(map[string]interface{})
	if entry["peer_id"] != testCelestrakID {
		t.Fatalf("wrong peer: %v", entry)
	}
	// The filtered stream re-indexes from zero.
	if entry["epm_index"].(float64) != 0 {
		t.Fatalf("epm_index = %v", entry["epm_index"])
	}
	if len(segments[0]) == 0 {
		t.Fatalf("empty stream for a peer with a stored EPM")
	}

	// Unknown filter: zero peers, empty stream (the flow answers 404).
	meta, segments = invoke(t, testOptions(t), "p2p.peers_snapshot", `{"peer_id":"16Uiu2Nope"}`)
	result = resultOf(t, meta)
	if len(result["peers"].([]interface{})) != 0 {
		t.Fatalf("expected no peers")
	}
	if len(segments[0]) != 0 {
		t.Fatalf("expected empty stream")
	}
}

func TestP2PStandardsSnapshot(t *testing.T) {
	meta, segments := invoke(t, testOptions(t), "p2p.standards_snapshot", "{}")
	result := resultOf(t, meta)
	entries := result["entries"].([]interface{})
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries (CAT + newest OMM), got %d: %v", len(entries), entries)
	}
	first := entries[0].(map[string]interface{})
	if first["standard"] != "CAT" || first["schema"] != "CAT.fbs" || first["peer_id"] != testCelestrakID {
		t.Fatalf("first entry: %v", first)
	}
	second := entries[1].(map[string]interface{})
	if second["standard"] != "OMM" {
		t.Fatalf("second entry: %v", second)
	}
	// Newest-per-(peer, standard): the OMM entry must be the NEW publication.
	if second["cid"] != "bafy-omm-new" {
		t.Fatalf("expected the newest OMM PNM, got cid=%v", second["cid"])
	}
	if second["file_id"] != "celestrak:gp:OMM.fbs:2026-07-06T03:00:00Z" {
		t.Fatalf("file_id = %v", second["file_id"])
	}

	// Stream frames verify as $PNM in entry order.
	stream := segments[0]
	index := 0
	for off := 0; off+4 <= len(stream); {
		n := int(binary.LittleEndian.Uint32(stream[off:]))
		frame := stream[off : off+4+n]
		if !PNM.SizePrefixedPNMBufferHasIdentifier(frame) {
			t.Fatalf("frame %d is not $PNM", index)
		}
		pnm := PNM.GetSizePrefixedRootAsPNM(frame, 0)
		entry := entries[index].(map[string]interface{})
		if string(pnm.FILE_ID()) != entry["file_id"] {
			t.Fatalf("frame %d FILE_ID %q != entry %v", index, pnm.FILE_ID(), entry["file_id"])
		}
		off += 4 + n
		index++
	}
	if index != 2 {
		t.Fatalf("pnm frames = %d", index)
	}
}

func TestP2PUnknownOperation(t *testing.T) {
	factory := NewP2PCapFactory(testOptions(t))
	handler := factory(&modulert.Module{})
	response, err := handler("p2p.write_something", []byte("{}"))
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	var meta map[string]interface{}
	if err := json.Unmarshal(response, &meta); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ok, _ := meta["ok"].(bool); ok {
		t.Fatalf("unknown op must fail: %v", meta)
	}
}

func TestP2PEmptyOptions(t *testing.T) {
	// A node without host/store/registry still answers with empty views.
	meta, segments := invoke(t, P2PCapOptions{}, "p2p.peers_snapshot", "")
	result := resultOf(t, meta)
	if len(result["peers"].([]interface{})) != 0 {
		t.Fatalf("expected no peers")
	}
	if len(segments) != 1 || len(segments[0]) != 0 {
		t.Fatalf("expected one empty stream segment")
	}
	meta, _ = invoke(t, P2PCapOptions{}, "p2p.standards_snapshot", "")
	result = resultOf(t, meta)
	if len(result["entries"].([]interface{})) != 0 {
		t.Fatalf("expected no entries")
	}
}

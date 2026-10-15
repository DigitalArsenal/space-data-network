package protocol_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/protocol"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/storefront"
)

// Security re-audit NET-02 (2026-10-15): the libp2p read lanes served every
// schema to any peer. These tests are the re-audit's repro
// (internal/zzsecaudit/flatsql_sync_read_test.go) turned into assertions:
// a storefront purchase request — buyer email, Stripe payment intent, a
// webhook URL carrying a token — must not leave the node over flatsql-sync
// or the SDS exchange to a peer the node has not authorized, while the
// public catalogue stays readable to anyone.

var purSecrets = [][]byte{[]byte("victim.buyer@example.com"), []byte("pi_SECRET_INTENT"), []byte("token=abc")}

type readFixture struct {
	store    *storage.FlatSQLStore
	server   host.Host
	client   host.Host
	purCID   string
	ommBytes []byte
}

func newReadFixture(t *testing.T) *readFixture {
	t.Helper()
	dir, err := os.MkdirTemp("", "sdn-net02-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(dir, "db"), validator)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sf, err := storefront.NewStore(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := sf.CreatePurchaseRequest(&storefront.PurchaseRequest{
		RequestID: "req-1", ListingID: "listing-1", TierName: "pro", BuyerPeerID: "12D3KooWBuyer",
		BuyerEmail: "victim.buyer@example.com", PaymentIntentID: "pi_SECRET_INTENT", WebhookURL: "https://buyer.example/hook?token=abc",
		PaymentAmount: 4200, PaymentCurrency: "USD", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if sds.IsPublicReadSchema("PUR.fbs") {
		t.Fatal("PUR.fbs is on the public read surface; the fixture proves nothing")
	}
	recs, err := store.QueryRawRecords(storage.RawRecordQuery{SchemaName: "PUR.fbs", Limit: 1})
	if err != nil || len(recs) != 1 {
		t.Fatalf("stored purchase request: %v (%d)", err, len(recs))
	}
	omm := sds.NewOMMBuilder().WithNoradCatID(25544).Build()
	if _, err := store.Store("OMM.fbs", omm, "12D3KooWProvider", nil); err != nil {
		t.Fatal(err)
	}
	server, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	client, err := libp2p.New(libp2p.NoListenAddrs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.Connect(ctx, peer.AddrInfo{ID: server.ID(), Addrs: server.Addrs()}); err != nil {
		t.Fatal(err)
	}
	return &readFixture{store: store, server: server, client: client, purCID: recs[0].CID, ommBytes: omm}
}

func (f *readFixture) syncRequest(t *testing.T, req map[string]any) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := f.client.NewStream(ctx, f.server.ID(), protocol.FlatSQLSyncProtocolID)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(10 * time.Second))
	if err := protocol.WriteFlatSQLSyncJSONFrame(s, req); err != nil {
		t.Fatal(err)
	}
	_ = s.CloseWrite()
	all, _ := io.ReadAll(io.LimitReader(s, 8<<20))
	return all
}

func leaksPurchase(b []byte) bool {
	for _, secret := range purSecrets {
		if bytes.Contains(b, secret) {
			return true
		}
	}
	return false
}

func refused(b []byte) bool { return bytes.Contains(b, []byte(`"status":"error"`)) }

func TestNET02FlatSQLSyncRefusesPrivateSchemasToUnauthorizedPeers(t *testing.T) {
	f := newReadFixture(t)
	f.server.SetStreamHandler(protocol.FlatSQLSyncProtocolID, protocol.NewFlatSQLSyncHandler(f.store).HandleStream)

	for name, req := range map[string]map[string]any{
		"read_chunk PUR":              {"op": "read_chunk", "schema": "PUR.fbs", "limit": 100},
		"read_chunk pur (lower case)": {"op": "read_chunk", "schema": "pur", "limit": 100},
		"read_chunk via schema_name":  {"op": "read_chunk", "schema_name": "PUR.fbs", "limit": 100},
		"read_chunk by record ref":    {"op": "read_chunk", "schema": "PUR.fbs", "records": []map[string]any{{"cid": f.purCID}}},
		"public schema, private ref":  {"op": "read_chunk", "schema": "OMM.fbs", "records": []map[string]any{{"cid": f.purCID, "schema": "PUR.fbs"}}},
		"scan PUR":                    {"op": "scan", "schema": "PUR.fbs", "limit": 100},
		"open_snapshot PUR":           {"op": "open_snapshot", "schema": "PUR.fbs"},
		"open_manifest PUR":           {"op": "open_manifest", "schema": "PUR.fbs"},
		"list_published_shards PUR":   {"op": "list_published_shards", "schema": "PUR.fbs"},
		"empty schema":                {"op": "read_chunk", "limit": 100},
		"other datastore":             {"op": "read_chunk", "schema": "OMM.fbs", "datastore_key": "private-ledger"},
		"list_datastores":             {"op": "list_datastores"},
	} {
		t.Run(name, func(t *testing.T) {
			out := f.syncRequest(t, req)
			if leaksPurchase(out) {
				t.Fatalf("unauthorized peer read purchase-request PII: %q", out)
			}
			if !refused(out) {
				t.Fatalf("request was not refused: %q", out)
			}
		})
	}

	// The public catalogue is still anonymous.
	out := f.syncRequest(t, map[string]any{"op": "read_chunk", "schema": "OMM.fbs", "limit": 10})
	if refused(out) || !bytes.Contains(out, f.ommBytes) {
		t.Fatalf("public OMM read was refused or empty: %q", out)
	}
}

func TestNET02FlatSQLSyncServesPrivateSchemasToAuthorizedPeers(t *testing.T) {
	f := newReadFixture(t)
	client := f.client.ID()
	f.server.SetStreamHandler(protocol.FlatSQLSyncProtocolID, protocol.NewFlatSQLSyncHandler(f.store).
		SetPrivateReadAuthorizer(func(id peer.ID) bool { return id == client }).HandleStream)
	out := f.syncRequest(t, map[string]any{"op": "read_chunk", "schema": "PUR.fbs", "limit": 100})
	if refused(out) || !bytes.Contains(out, purSecrets[0]) {
		t.Fatalf("authorized peer could not read its private schema: %q", out)
	}
}

func sdsExchange(t *testing.T, f *readFixture, msg []byte) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := f.client.NewStream(ctx, f.server.ID(), protocol.SDSProtocolID)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := s.Write(msg); err != nil {
		t.Fatal(err)
	}
	_ = s.CloseWrite()
	all, _ := io.ReadAll(io.LimitReader(s, 8<<20))
	return all
}

func queryMsg(schema string) []byte {
	m := []byte{protocol.MsgQuery}
	m = binary.BigEndian.AppendUint16(m, uint16(len(schema)))
	m = append(m, schema...)
	q := "SELECT *"
	m = binary.BigEndian.AppendUint32(m, uint32(len(q)))
	return append(m, q...)
}

func requestMsg(schema, cid string) []byte {
	m := []byte{protocol.MsgRequestData}
	m = binary.BigEndian.AppendUint16(m, uint16(len(schema)))
	m = append(m, schema...)
	m = binary.BigEndian.AppendUint16(m, uint16(len(cid)))
	return append(m, cid...)
}

func TestNET02SDSExchangeRefusesPrivateSchemasToUnauthorizedPeers(t *testing.T) {
	f := newReadFixture(t)
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	h := protocol.NewSDSExchangeHandler(f.store, validator)
	// Even a node that accepts pushes from anyone has not published its
	// private tables: reads have their own authorizer.
	h.SetPushAuthorizer(protocol.AllowAnyPush)
	f.server.SetStreamHandler(protocol.SDSProtocolID, h.HandleStream)

	for name, msg := range map[string][]byte{
		"MsgQuery PUR":       queryMsg("PUR.fbs"),
		"MsgRequestData PUR": requestMsg("PUR.fbs", f.purCID),
	} {
		out := sdsExchange(t, f, msg)
		if leaksPurchase(out) {
			t.Fatalf("%s: unauthorized peer read purchase-request PII", name)
		}
		if len(out) != 1 || out[0] != protocol.RespReject {
			t.Fatalf("%s: response %x, want a single RespReject", name, out)
		}
	}
	if out := sdsExchange(t, f, queryMsg("OMM.fbs")); len(out) == 0 || out[0] != protocol.RespAccept || !bytes.Contains(out, f.ommBytes) {
		t.Fatalf("public OMM query refused: %x", out)
	}

	client := f.client.ID()
	h.SetPrivateReadAuthorizer(func(id peer.ID) bool { return id == client })
	if out := sdsExchange(t, f, queryMsg("PUR.fbs")); len(out) == 0 || out[0] != protocol.RespAccept || !bytes.Contains(out, purSecrets[0]) {
		t.Fatalf("authorized private query refused: %x", out)
	}
	if out := sdsExchange(t, f, requestMsg("PUR.fbs", f.purCID)); len(out) == 0 || out[0] != protocol.RespAccept {
		t.Fatalf("authorized private request refused: %x", out)
	}
}

// NET-06: wire_speed_probe streamed up to 512 MiB of filler to anyone.
func TestNET06WireSpeedProbeNeedsAuthorization(t *testing.T) {
	f := newReadFixture(t)
	f.server.SetStreamHandler(protocol.FlatSQLSyncProtocolID, protocol.NewFlatSQLSyncHandler(f.store).HandleStream)
	out := f.syncRequest(t, map[string]any{"op": "wire_speed_probe", "probe_bytes": 1 << 20})
	if !refused(out) || len(out) > 4096 {
		t.Fatalf("unauthorized probe was served (%d bytes)", len(out))
	}
	client := f.client.ID()
	f.server.SetStreamHandler(protocol.FlatSQLSyncProtocolID, protocol.NewFlatSQLSyncHandler(f.store).
		SetPrivateReadAuthorizer(func(id peer.ID) bool { return id == client }).HandleStream)
	out = f.syncRequest(t, map[string]any{"op": "wire_speed_probe", "probe_bytes": 1 << 20})
	if refused(out) || len(out) < 1<<20 {
		t.Fatalf("authorized probe refused (%d bytes): %.200q", len(out), out)
	}
}

// echoSyncLog stands in for logservice.SyncHandler: it reads the request's
// schema name the way the real handler does and answers accept + that name.
type echoSyncLog struct{ called chan string }

func (e *echoSyncLog) HandleSyncLog(s network.Stream) {
	var n [2]byte
	if _, err := io.ReadFull(s, n[:]); err != nil {
		return
	}
	name := make([]byte, binary.BigEndian.Uint16(n[:]))
	if _, err := io.ReadFull(s, name); err != nil {
		return
	}
	e.called <- string(name)
	_, _ = s.Write(append([]byte{0x01}, name...))
}

func syncLogMsg(schema string) []byte {
	m := []byte{protocol.MsgSyncLog}
	m = binary.BigEndian.AppendUint16(m, uint16(len(schema)))
	m = append(m, schema...)
	m = binary.BigEndian.AppendUint16(m, 0) // publisher
	m = binary.LittleEndian.AppendUint64(m, 0)
	return binary.LittleEndian.AppendUint32(m, 10)
}

// NET-02 on the SDS exchange's third read lane: MsgSyncLog served any
// schema's publication log (record CIDs, entity ids, timestamps) to anyone.
func TestNET02SyncLogRefusesPrivateSchemasToUnauthorizedPeers(t *testing.T) {
	f := newReadFixture(t)
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	h := protocol.NewSDSExchangeHandler(f.store, validator)
	logs := &echoSyncLog{called: make(chan string, 4)}
	h.SetSyncHandler(logs)
	f.server.SetStreamHandler(protocol.SDSProtocolID, h.HandleStream)

	for _, schema := range []string{"PUR.fbs", "pur", "../PUR.fbs", ""} {
		if out := sdsExchange(t, f, syncLogMsg(schema)); len(out) != 1 || out[0] != 0x00 {
			t.Fatalf("sync log %q: response %x, want a single refusal", schema, out)
		}
	}
	select {
	case got := <-logs.called:
		t.Fatalf("the log handler ran for %q", got)
	default:
	}
	// A public schema reaches the handler with its request intact.
	if out := sdsExchange(t, f, syncLogMsg("OMM.fbs")); string(out) != "\x01OMM.fbs" {
		t.Fatalf("public sync log: %q", out)
	}
	client := f.client.ID()
	h.SetPrivateReadAuthorizer(func(id peer.ID) bool { return id == client })
	if out := sdsExchange(t, f, syncLogMsg("PUR.fbs")); string(out) != "\x01PUR.fbs" {
		t.Fatalf("authorized private sync log: %q", out)
	}
}

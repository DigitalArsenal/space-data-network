package protocol

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/CAT"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OCM"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

// These streams contain no identity keys, sessions, or private-read grants.
type publicReadTestConn struct{ network.Conn }

func (publicReadTestConn) RemotePeer() peer.ID { return "" }

type publicReadTestStream struct {
	network.Stream
	input  *bytes.Reader
	output bytes.Buffer
}

func (s *publicReadTestStream) Read(p []byte) (int, error)      { return s.input.Read(p) }
func (s *publicReadTestStream) Write(p []byte) (int, error)     { return s.output.Write(p) }
func (s *publicReadTestStream) Conn() network.Conn              { return publicReadTestConn{} }
func (s *publicReadTestStream) Close() error                    { return nil }
func (s *publicReadTestStream) Reset() error                    { return nil }
func (s *publicReadTestStream) SetReadDeadline(time.Time) error { return nil }

func TestAnonymousRecordReadClassification(t *testing.T) {
	public := map[string]bool{}
	for _, schema := range []string{"APP.fbs", "CAT.fbs", "DPM.fbs", "EGP.fbs", "EPM.fbs", "LKS.fbs", "MPE.fbs", "NCD.fbs", "OCM.fbs", "OMM.fbs", "PNM.fbs", "RFB.fbs", "SPW.fbs", "WXF.fbs"} {
		public[schema] = true
	}
	h := NewFlatSQLSyncHandler(nil)
	exchange := NewSDSExchangeHandler(nil, nil)
	for _, schema := range append(append([]string{}, sds.SupportedSchemas...), "NOPE.fbs", "", "../OCM") {
		for _, op := range []string{syncwire.OpReadChunk, syncwire.OpOpenManifest} {
			req := syncRequest{op: op, schema: schema}
			if allowed := h.authorize("", &req) == nil; allowed != public[schema] {
				t.Errorf("%s %s allowed=%v, want %v", op, schema, allowed, public[schema])
			}
		}
		if _, allowed := exchange.mayRead("", schema); allowed != public[schema] {
			t.Errorf("SDS exchange %s allowed=%v, want %v", schema, allowed, public[schema])
		}
	}
}

func TestAnonymousOCMAndCATRecordReads(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewFlatSQLStore(t.TempDir(), validator)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	h := NewFlatSQLSyncHandler(store)
	exchange := NewSDSExchangeHandler(store, validator)
	for _, schema := range []string{"OCM.fbs", "CAT.fbs"} {
		for _, source := range []string{"catalog", "independent-products"} {
			t.Run(schema+"/"+source, func(t *testing.T) {
				b := flatbuffers.NewBuilder(256)
				name := b.CreateString("synthetic-" + source)
				var root flatbuffers.UOffsetT
				if schema == "OCM.fbs" {
					OCM.MetadataStart(b)
					OCM.MetadataAddOBJECT_NAME(b, name)
					meta := OCM.MetadataEnd(b)
					OCM.OCMStart(b)
					OCM.OCMAddMETADATA(b, meta)
					root = OCM.OCMEnd(b)
				} else {
					CAT.CATStart(b)
					CAT.CATAddOBJECT_NAME(b, name)
					root = CAT.CATEnd(b)
				}
				b.FinishWithFileIdentifier(root, []byte("$"+schema[:3]))
				record := b.FinishedBytes()
				cid, err := store.StoreWithSourceTags(schema, record, "synthetic", nil, storage.SourceTags{ProviderID: "synthetic", SourceName: source})
				if err != nil {
					t.Fatal(err)
				}
				for _, op := range []string{syncwire.OpReadChunk, syncwire.OpOpenManifest} {
					var request bytes.Buffer
					if err := syncwire.Request(&request, syncwire.QRPFields{SchemaName: schema, ProviderID: "synthetic", SourceName: source, Limit: 10}); err != nil {
						t.Fatal(err)
					}
					s := &publicReadTestStream{input: bytes.NewReader(request.Bytes())}
					h.HandleStream(op, s)
					if op == syncwire.OpReadChunk {
						if !bytes.Contains(s.output.Bytes(), record) {
							t.Fatalf("%s omitted original record bytes", op)
						}
					} else {
						q, err := syncwire.ParseQRP(s.output.Bytes())
						if err != nil {
							t.Fatal(err)
						}
						segments, err := syncwire.Segments(q)
						if err != nil || len(segments) != 1 || segments[0].RecordCount != 1 {
							t.Fatalf("manifest segments=%v, error=%v", segments, err)
						}
					}
				}
				var request bytes.Buffer
				for _, value := range []string{schema, cid} {
					if err := binary.Write(&request, binary.BigEndian, uint16(len(value))); err != nil {
						t.Fatal(err)
					}
					request.WriteString(value)
				}
				s := &publicReadTestStream{input: bytes.NewReader(request.Bytes())}
				exchange.handleDataRequest(context.Background(), s)
				got := s.output.Bytes()
				if len(got) < 5 || got[0] != RespAccept || int(binary.BigEndian.Uint32(got[1:5])) != len(record) || !bytes.Equal(got[5:], record) {
					t.Fatal("SDS exchange did not return original record bytes")
				}
			})
		}
	}
}

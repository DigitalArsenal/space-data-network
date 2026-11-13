package epm

import (
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"

	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// The JS E2E builds this test binary and starts it directly, then closes stdin
// to stop both hosts. No profile or artifact is signed by this fixture.
func TestPeerEPMInteropHost(t *testing.T) {
	dir := os.Getenv("SDN_EPM_INTEROP_DIR")
	if dir == "" {
		t.Skip("fixture started by sdn-js peer-epm E2E")
	}
	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"), libp2p.DisableRelay())
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	b := flatbuffers.NewBuilder(128)
	name := b.CreateString("EPM exchange interoperability")
	EPM.EPMStart(b)
	EPM.EPMAddDN(b, name)
	root := EPM.EPMEnd(b)
	EPM.FinishSizePrefixedEPMBuffer(b, root)
	data := b.FinishedBytes()
	registry := peers.NewRegistry(false, nil)
	if err := registry.AddPeer(&peers.TrustedPeer{ID: h.ID(), EPMData: data, TrustLevel: peers.Standard}); err != nil {
		t.Fatal(err)
	}
	svc := &Service{registry: registry, epmBytes: data}
	svc.RegisterProtocol(h)
	bad, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"), libp2p.DisableRelay())
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Close()
	var call atomic.Int32
	done := make(chan struct{})
	defer close(done)
	bad.SetStreamHandler(EPMExchangeProtocolID, func(s network.Stream) {
		defer s.Close()
		_ = s.SetDeadline(time.Now().Add(30 * time.Second))
		var request [4]byte
		if _, err := io.ReadFull(s, request[:]); err != nil {
			return
		}
		header := make([]byte, 8)
		switch call.Add(1) {
		case 1:
			header = header[:3]
		case 2:
			binary.LittleEndian.PutUint32(header, 9)
		case 3:
			binary.LittleEndian.PutUint32(header[4:], 65537)
		case 4:
			binary.LittleEndian.PutUint32(header[4:], 2)
			header = append(header, 1)
		case 5:
			binary.LittleEndian.PutUint32(header[4:], 1)
			header = append(header, 1, 2)
		case 6: // Empty success.
		case 7:
			binary.LittleEndian.PutUint32(header, 1)
			binary.LittleEndian.PutUint32(header[4:], 1)
		case 8:
			binary.LittleEndian.PutUint32(header, 2)
		case 9: // Keep a partial header open until the JS test cancels its stream.
			_, _ = s.Write([]byte{0})
			_ = os.WriteFile(filepath.Join(dir, "cancel-ready"), nil, 0644)
			<-done
			return
		}
		_, _ = s.Write(header)
	})
	for name, bytes := range map[string][]byte{"profile.epm": data, "valid.addr": []byte(h.Addrs()[0].String() + "/p2p/" + h.ID().String()), "invalid.addr": []byte(bad.Addrs()[0].String() + "/p2p/" + bad.ID().String())} {
		if err := os.WriteFile(filepath.Join(dir, name), bytes, 0644); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
}

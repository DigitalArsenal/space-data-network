package moduledeliveryplugin

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/spacedatanetwork/sdn-server/internal/license"
	"github.com/spacedatanetwork/sdn-server/internal/moduledelivery"
	"github.com/spacedatanetwork/sdn-server/plugins"
)

const ID = "spaceaware-module-delivery"

// Plugin wires the module-delivery service into the generic plugin runtime.
type Plugin struct {
	mu           sync.RWMutex
	host         host.Host
	service      *moduledelivery.Service
	adminAPI     *license.APIHandler
	discoveryCID string
}

// New returns a new unstarted module-delivery plugin.
func New() *Plugin {
	return &Plugin{}
}

func (p *Plugin) ID() string {
	return ID
}

func (p *Plugin) Start(ctx context.Context, runtime plugins.RuntimeContext) error {
	if runtime.Host == nil {
		return fmt.Errorf("%s plugin requires libp2p host", ID)
	}
	if strings.EqualFold(strings.TrimSpace(runtime.Mode), "edge") {
		return nil
	}
	basePath := strings.TrimSpace(runtime.BaseDataPath)
	if basePath == "" {
		return fmt.Errorf("%s plugin requires non-empty base data path", ID)
	}

	providerPubKey, err := hostPublicKeyBytes(runtime.Host)
	if err != nil {
		return err
	}
	svc, err := moduledelivery.NewService(basePath, runtime.PeerID, runtime.PeerID, providerPubKey, runtime.IPFSAPIURL)
	if err != nil {
		return err
	}
	discoveryCID, err := moduledelivery.ComputeDiscoveryCID(providerPubKey)
	if err != nil {
		_ = svc.Close()
		return err
	}

	runtime.Host.SetStreamHandler(protocol.ID(moduledelivery.ProtocolID), svc.HandleStream)

	p.mu.Lock()
	p.host = runtime.Host
	p.service = svc
	p.adminAPI = license.NewAPIHandler(svc.LicenseService())
	p.discoveryCID = discoveryCID.String()
	p.mu.Unlock()

	_ = ctx
	return nil
}

func (p *Plugin) RegisterRoutes(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	p.mu.RLock()
	adminAPI := p.adminAPI
	p.mu.RUnlock()
	if adminAPI != nil {
		adminAPI.RegisterAdminRoutes(mux)
	}
}

func (p *Plugin) Close() error {
	p.mu.Lock()
	h := p.host
	svc := p.service
	p.host = nil
	p.service = nil
	p.adminAPI = nil
	p.discoveryCID = ""
	p.mu.Unlock()

	if h != nil {
		h.RemoveStreamHandler(protocol.ID(moduledelivery.ProtocolID))
	}
	if svc != nil {
		return svc.Close()
	}
	return nil
}

func (p *Plugin) Service() *moduledelivery.Service {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.service
}

func (p *Plugin) LicenseService() *license.Service {
	svc := p.Service()
	if svc == nil {
		return nil
	}
	return svc.LicenseService()
}

func (p *Plugin) TokenVerifier() *license.TokenVerifier {
	licenseSvc := p.LicenseService()
	if licenseSvc == nil {
		return nil
	}
	return licenseSvc.Verifier()
}

func (p *Plugin) DiscoveryCID() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.discoveryCID
}

func hostPublicKeyBytes(h host.Host) ([]byte, error) {
	if h == nil {
		return nil, fmt.Errorf("libp2p host is required")
	}
	pub := h.Peerstore().PubKey(h.ID())
	if pub == nil {
		var err error
		pub, err = h.ID().ExtractPublicKey()
		if err != nil {
			return nil, fmt.Errorf("extract provider public key: %w", err)
		}
	}
	raw, err := pub.Raw()
	if err != nil {
		return nil, fmt.Errorf("marshal provider public key: %w", err)
	}
	if len(raw) != 33 || (raw[0] != 0x02 && raw[0] != 0x03) {
		return nil, fmt.Errorf("provider public key must be 33-byte compressed secp256k1")
	}
	return append([]byte(nil), raw...), nil
}

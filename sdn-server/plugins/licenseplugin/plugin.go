package licenseplugin

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/spacedatanetwork/sdn-server/internal/license"
	"github.com/spacedatanetwork/sdn-server/plugins"
)

const (
	// ID is the canonical plugin identifier for the built-in license server plugin.
	ID = "spaceaware-license"
)

// Plugin wraps the SDN license service into the generic plugin runtime contract.
type Plugin struct {
	mu      sync.RWMutex
	host    host.Host
	service *license.Service
	api     *license.APIHandler
}

// New returns a new unstarted license plugin.
func New() *Plugin {
	return &Plugin{}
}

// ID returns the plugin identifier.
func (p *Plugin) ID() string {
	return ID
}

// Start initializes the license service and installs the libp2p stream handler.
func (p *Plugin) Start(ctx context.Context, runtime plugins.RuntimeContext) error {
	if runtime.Host == nil {
		return fmt.Errorf("%s plugin requires libp2p host", ID)
	}

	// Skip plugin runtime for edge mode while keeping registration stable.
	if strings.EqualFold(strings.TrimSpace(runtime.Mode), "edge") {
		return nil
	}

	basePath := strings.TrimSpace(runtime.BaseDataPath)
	if basePath == "" {
		return fmt.Errorf("%s plugin requires non-empty base data path", ID)
	}

	svc, err := license.NewService(basePath, runtime.PeerID)
	if err != nil {
		return err
	}

	runtime.Host.SetStreamHandler(protocol.ID(license.ProtocolID), svc.HandleStream)

	p.mu.Lock()
	p.host = runtime.Host
	p.service = svc
	p.api = license.NewAPIHandler(svc)
	p.mu.Unlock()

	_ = ctx // reserved for future plugin background jobs.
	return nil
}

// RegisterRoutes mounts HTTP routes for license and plugin-delivery APIs.
func (p *Plugin) RegisterRoutes(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	p.mu.RLock()
	apiHandler := p.api
	p.mu.RUnlock()
	if apiHandler != nil {
		apiHandler.RegisterRoutes(mux)
	}
}

// Close stops the plugin and releases resources.
func (p *Plugin) Close() error {
	p.mu.Lock()
	h := p.host
	svc := p.service
	p.host = nil
	p.service = nil
	p.api = nil
	p.mu.Unlock()

	if h != nil {
		h.RemoveStreamHandler(protocol.ID(license.ProtocolID))
	}
	if svc != nil {
		return svc.Close()
	}
	return nil
}

// Service returns the underlying license service instance.
func (p *Plugin) Service() *license.Service {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.service
}

// TokenVerifier returns the token verifier from the underlying license service.
func (p *Plugin) TokenVerifier() *license.TokenVerifier {
	svc := p.Service()
	if svc == nil {
		return nil
	}
	return svc.Verifier()
}

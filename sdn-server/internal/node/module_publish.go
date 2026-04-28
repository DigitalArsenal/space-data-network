package node

import (
	"fmt"
	"os"
	"strings"

	"github.com/libp2p/go-libp2p/core/network"
	libp2pprotocol "github.com/libp2p/go-libp2p/core/protocol"

	"github.com/spacedatanetwork/sdn-server/internal/license"
)

func (n *Node) registerModulePublishHandler() {
	if n == nil || n.host == nil || n.pluginRegistry == nil {
		return
	}
	n.host.SetStreamHandler(
		libp2pprotocol.ID(license.ModulePublishProtocolID),
		n.handleModulePublishStream,
	)
	log.Infof("Module publish API available over libp2p protocol %s", license.ModulePublishProtocolID)
}

func (n *Node) handleModulePublishStream(stream network.Stream) {
	defer stream.Close()

	token := modulePublishToken()
	if strings.TrimSpace(token) == "" {
		license.WriteModulePublishResponse(stream, license.ModulePublishResponse{
			OK:    false,
			Error: "module publish token is not configured",
		})
		return
	}
	if n.pluginRegistry == nil {
		license.WriteModulePublishResponse(stream, license.ModulePublishResponse{
			OK:    false,
			Error: "plugin registry is unavailable",
		})
		return
	}
	if n.licensingModule == nil {
		license.WriteModulePublishResponse(stream, license.ModulePublishResponse{
			OK:    false,
			Error: "licensing module is unavailable",
		})
		return
	}

	response := license.ApplyModulePublishJSON(stream, n.pluginRegistry, token)
	if response.OK {
		if err := bootstrapLicensingModule(n.licensingModule, n.pluginRegistry); err != nil {
			response = license.ModulePublishResponse{
				OK:    false,
				Error: fmt.Sprintf("catalog updated but licensing runtime publish failed: %v", err),
			}
		}
	}
	license.WriteModulePublishResponse(stream, response)
}

func modulePublishToken() string {
	if token := strings.TrimSpace(os.Getenv("SDN_MODULE_PUBLISH_TOKEN")); token != "" {
		return token
	}
	return strings.TrimSpace(os.Getenv("SDN_LICENSE_ADMIN_TOKEN"))
}

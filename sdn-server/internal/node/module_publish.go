package node

import (
	"fmt"

	"github.com/libp2p/go-libp2p/core/network"
	libp2pprotocol "github.com/libp2p/go-libp2p/core/protocol"

	"github.com/spacedatanetwork/sdn-server/internal/license"
)

// SetModulePublishAuthorizer sets the wallet-admin lookup used by the libp2p
// module publish protocol.
func (n *Node) SetModulePublishAuthorizer(authorizer license.ModulePublishAuthorizer) {
	if n == nil {
		return
	}
	n.modulePublishAuthorizer = authorizer
}

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

	if n.modulePublishAuthorizer == nil {
		license.WriteModulePublishResponse(stream, license.ModulePublishResponse{
			OK:    false,
			Error: "module publish wallet authorizer is unavailable",
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

	response := license.ApplyModulePublishJSON(stream, n.pluginRegistry, n.modulePublishAuthorizer)
	if response.OK {
		// Publish exactly the modules this request changed, and nothing else.
		//
		// This used to call bootstrapLicensingModule, which begins with
		// `server_configure_runtime` — a full session reset that drops every
		// pending challenge, every issued grant and every publication in the key
		// server, then re-provisions the entire catalog over dozens of serial
		// guest invocations. A publish is not a reconfiguration, and treating it
		// as one meant that any browser loading the gallery while an unrelated
		// module was published lost its module on the first attempt. See
		// graph/tasks/sdn-delivery-first-attempt-bar-remaining-modes.md.
		changed := make([]string, 0, len(response.Results))
		for _, result := range response.Results {
			changed = append(changed, result.ID)
		}
		if err := publishCatalogAssets(n.licensingModule, n.pluginRegistry, changed); err != nil {
			response = license.ModulePublishResponse{
				OK:    false,
				Error: fmt.Sprintf("catalog updated but licensing runtime publish failed: %v", err),
			}
		}
	}
	license.WriteModulePublishResponse(stream, response)
}

package node

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/spacedatanetwork/sdn-server/internal/license"
)

// SettledEntitlementBridge projects paid storefront module entitlements into
// the provider's persisted plugin registry and its already-running licensing
// WASM module. It is application-blind: module IDs and requester xpubs arrive
// from signed marketplace/runtime records, never from host policy literals.
type SettledEntitlementBridge struct {
	node *Node
	mu   sync.Mutex
}

// NewSettledEntitlementBridge binds the bridge to one running node. A node
// without a plugin registry can still host storefront data, so such listings
// are ignored until they map to a registry asset.
func NewSettledEntitlementBridge(n *Node) *SettledEntitlementBridge {
	return &SettledEntitlementBridge{node: n}
}

// ProvisionSettledEntitlement persists the exact requester xpub used by the
// licensing challenge and republishes only the affected module. Repeated calls
// still reconcile the runtime, allowing a completion retry to recover from a
// prior guest-invocation failure without duplicating the catalog entry.
func (b *SettledEntitlementBridge) ProvisionSettledEntitlement(ctx context.Context, moduleID, buyerXPub string) error {
	return b.mutateAndPublish(ctx, moduleID, buyerXPub, true)
}

// RevokeSettledEntitlement removes the requester xpub and immediately updates
// the running licensing module. When the removal empties a fail-closed
// allowlist, the current licensing ABI has no single-publication delete method;
// the bridge reconfigures the guest and republishes the admitted catalog so the
// stale key cannot remain reachable. No daemon restart is required.
func (b *SettledEntitlementBridge) RevokeSettledEntitlement(ctx context.Context, moduleID, buyerXPub string) error {
	return b.mutateAndPublish(ctx, moduleID, buyerXPub, false)
}

func (b *SettledEntitlementBridge) mutateAndPublish(ctx context.Context, moduleID, buyerXPub string, grant bool) error {
	if b == nil || b.node == nil || b.node.pluginRegistry == nil {
		return nil
	}
	moduleID = strings.TrimSpace(moduleID)
	if moduleID == "" {
		return nil
	}
	buyerXPub = strings.TrimSpace(buyerXPub)
	if buyerXPub == "" {
		return errors.New("requester xpub is required")
	}
	if ctx != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	reg := b.node.pluginRegistry
	asset, ok := reg.Get(moduleID)
	if !ok {
		// A storefront can describe data or a module served elsewhere. The
		// bridge applies only when the listing maps to this node's registry.
		return nil
	}
	resolved := reg.GrantPolicyConfig().Resolve(asset.ID, asset.GrantPolicy)
	if resolved.Policy != license.GrantPolicyAllowlist {
		// Explicit open/link-key modules remain open. Adding a paid buyer to one
		// would silently narrow the gallery or another deliberately open lane.
		return nil
	}

	var err error
	if grant {
		_, err = reg.GrantAllowedXpub(moduleID, buyerXPub)
	} else {
		_, err = reg.RevokeAllowedXpub(moduleID, buyerXPub)
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if b.node.licensingModule == nil {
		return fmt.Errorf("licensing runtime is unavailable for module %q", moduleID)
	}

	decision, ok := reg.PublicationDecision(moduleID)
	if !ok {
		return nil
	}
	if decision.Publish {
		return publishCatalogAssets(b.node.licensingModule, reg, []string{moduleID})
	}

	// server_publish_module can replace a publication but cannot remove one,
	// and publishing an empty ALLOWED_XPUBS vector would mean unrestricted.
	// Reconfigure the in-process guest to clear the stale publication, then
	// restore every still-admitted module from the persisted registry.
	return bootstrapLicensingModule(b.node.licensingModule, reg)
}

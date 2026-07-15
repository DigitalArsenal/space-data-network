package sdnservices

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ipfs/kubo/sdn/credstore"
	"github.com/ipfs/kubo/sdn/modulert"
)

// NewSecretsCapFactory returns a bridge-aware BridgeCapFactory serving the
// "secrets" hostcall prefix, backed by the node's encrypted-at-rest credential
// keystore (sdn/credstore). It is the kubo-native port of the sdn-server
// secrets capability (internal/modulert/caps/secrets.go); it lives here in
// sdnservices alongside the other capability factories (storage_cap.go,
// http_cap.go, pubsub_cap.go) and reuses this package's cap JSON envelope
// (capsjson.go).
//
// # WHY THIS CAPABILITY EXPORTS PLAINTEXT (AND wallet_sign/keyslot DOES NOT)
//
// The keyslot/wallet_sign capability is a crypto oracle: it returns signatures
// and unwrapped payloads, never the slot's key bytes, because every operation a
// guest needs can be expressed as "ask the host to do the private-key step for
// me".
//
// A Space-Track credential admits no such formulation. The module's job is to
// present `identity` and `password` to Space-Track's own login form; the secret
// IS the message. So this capability does export the credential, and its safety
// rests entirely on the gate in front of it:
//
//   - the module must be approved BY CONTENT HASH for the specific lane
//     (capability "secrets:spacetrack"), by an operator, in the node's
//     capability policy. The whole "secrets:" family is
//     modulert.IsSensitiveCapability, so absent an approval entry the module is
//     DENIED AT LOAD — the module fails to load, not just this call.
//   - approval is PER LANE. A module approved for "secrets:spacetrack" cannot
//     read the EDC or MyIntelsat credential: the per-operation check below tests
//     the capability for the exact id requested, mirroring the
//     storage_query/storage_write split in storage_cap.go.
//
// RESIDUAL RISK, STATED PLAINLY: an operator who approves "secrets:spacetrack"
// for a module hash has given that exact module the credential. If that module
// also holds "http", it can exfiltrate it. Approval is the trust decision; this
// code only guarantees that the decision is explicit, per-module, and per-lane.
func NewSecretsCapFactory(store *credstore.Store) modulert.BridgeCapFactory {
	return func(mod *modulert.Module, bridge *modulert.HostBridge) modulert.CapHandler {
		return func(operation string, payload []byte) ([]byte, error) {
			switch operation {
			case "secrets.get":
				return handleSecretsGet(store, bridge, payload), nil
			case "secrets.status":
				return handleSecretsStatus(store, bridge, payload), nil
			default:
				// No "secrets.list" and no "secrets.export": a guest may never
				// enumerate the keystore, only ask for a lane it is already
				// approved for.
				return errCapJSON(fmt.Sprintf("unknown secrets operation: %s", operation)), nil
			}
		}
	}
}

// CapabilityForID is the capability name gating one credential lane.
// Approving "secrets:spacetrack" grants exactly the spacetrack credential. It is
// the name BuildServices registers the secrets factory under (one per
// credstore.AllIDs lane) and matches modulert.SecretsCapabilityPrefix.
func CapabilityForID(id string) string {
	return "secrets:" + strings.TrimSpace(id)
}

// handleSecretsGet returns the credential for a requested lane.
//
//	request:  {"id":"spacetrack"}
//	response: {"id":"...","username":"...","secret":"..."}
//
// Fail closed at every step: no store, no bridge, blank id, unapproved lane, or
// unconfigured credential all return an error and no material.
func handleSecretsGet(store *credstore.Store, bridge *modulert.HostBridge, payload []byte) []byte {
	var request struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(payload, &request); err != nil {
		return errCapJSON("invalid secrets.get request payload")
	}
	id := strings.TrimSpace(request.ID)
	if id == "" {
		return errCapJSON("secrets.get requires an id")
	}

	// PER-LANE GATE. The load-time policy check already proved the operator
	// approved SOME secrets:* capability for this module hash; this proves the
	// module was approved for THIS lane. A module holding secrets:spacetrack
	// asking for "edc_cpf" is denied here.
	capability := CapabilityForID(id)
	if bridge == nil || !bridge.HasCapability(capability) {
		return errCapJSON(fmt.Sprintf("secrets.get requires the %s capability grant", capability))
	}

	if store == nil {
		return errCapJSON("credential store is not available on this node")
	}

	cred, err := store.Reveal(id)
	if err != nil {
		// Never echo the underlying error: it is a keystore/crypto error and
		// carries no credential, but stay conservative.
		return errCapJSON(fmt.Sprintf("credential %q is not configured on this node", id))
	}

	// The plaintext crosses the sandbox boundary here, and ONLY here, for a
	// module the operator approved for this exact lane.
	return okCapJSON(map[string]interface{}{
		"id":       id,
		"username": cred.Username,
		"secret":   cred.Secret.Reveal(),
	})
}

// handleSecretsStatus reports whether a lane is configured WITHOUT returning the
// credential. It is gated by the same per-lane capability: an unapproved module
// must not be able to probe which credentials the node holds.
//
//	request:  {"id":"spacetrack"}
//	response: {"id":"spacetrack","configured":true,"username_masked":"o***@example.com",...}
func handleSecretsStatus(store *credstore.Store, bridge *modulert.HostBridge, payload []byte) []byte {
	var request struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(payload, &request); err != nil {
		return errCapJSON("invalid secrets.status request payload")
	}
	id := strings.TrimSpace(request.ID)
	if id == "" {
		return errCapJSON("secrets.status requires an id")
	}

	capability := CapabilityForID(id)
	if bridge == nil || !bridge.HasCapability(capability) {
		return errCapJSON(fmt.Sprintf("secrets.status requires the %s capability grant", capability))
	}
	if store == nil {
		return errCapJSON("credential store is not available on this node")
	}

	status, err := store.Status(id)
	if err != nil {
		return errCapJSON("credential store is unreadable")
	}
	// credstore.Status carries no secret material.
	return okCapJSON(status)
}

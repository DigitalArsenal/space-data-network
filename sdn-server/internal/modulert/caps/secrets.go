package caps

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/credstore"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
)

// NewSecretsCapFactory returns a bridge-aware CapFactory serving the "secrets"
// hostcall prefix, backed by the node's encrypted-at-rest credential keystore.
//
// # WHY THIS CAPABILITY EXPORTS PLAINTEXT (AND KEYSLOT DOES NOT)
//
// caps/keyslot.go is a crypto oracle: it returns signatures and unwrapped
// payloads, never the slot's key bytes, because every operation a guest needs
// can be expressed as "ask the host to do the private-key step for me".
//
// A Space-Track credential admits no such formulation. The module's job is to
// present `identity` and `password` to Space-Track's own login form; the secret
// IS the message. The only way to keep the plaintext away from the guest
// entirely would be for the host to perform the login and hand back the session
// cookie — which merely substitutes one bearer secret for another, while moving
// the provider's whole HTTP contract into the daemon.
//
// So this capability does export the credential, and its safety rests entirely
// on the gate in front of it:
//
//   - the module must be approved BY CONTENT HASH for the specific lane
//     (capability "secrets:spacetrack"), by an operator, in
//     capability_policy.json. The whole "secrets:" prefix is sensitive
//     (modulert.IsSensitiveCapability), so absent an approval entry the module
//     is DENIED AT LOAD — the whole module fails to load, not just this call.
//   - approval is PER LANE. A module approved for "secrets:spacetrack" cannot
//     read the EDC or MyIntelsat credential: the per-operation check below
//     tests the capability for the exact id requested, mirroring the
//     storage_query/storage_write split in caps/storage.go.
//
// LANES ARE OPERATOR-DEFINED (owner 2026-08-04). Nothing in this file knows or
// cares which lanes exist: an id the operator invented last night behaves
// exactly like "spacetrack" — same prefix gate at load, same per-lane check per
// call, same isolation between lanes. The well-known ids are only the ones the
// node ships a verifier for; they carry no privilege here.
//
// RESIDUAL RISK, STATED PLAINLY: an operator who approves "secrets:spacetrack"
// for a module hash has given that exact module the credential. If that module
// also holds "http", it can exfiltrate it. Approval is the trust decision; this
// code only guarantees that the decision is explicit, per-module, and per-lane.
func NewSecretsCapFactory(store *credstore.Store) modulert.BridgeCapFactory {
	return NewSecretsCapFactoryWithAudit(store, nil)
}

// NewSecretsCapFactoryWithAudit is NewSecretsCapFactory with the node's shared
// activity ring attached. Reads stay untapped (they are the module's normal
// working path); WRITES are recorded, because a module changing what the node
// holds is an operator-visible event. The ring never carries a value — only
// the lane id and the outcome. A nil ring is a no-op (ActivityRing.Append is
// nil-safe), which is why the plain constructor above still compiles for every
// existing caller.
func NewSecretsCapFactoryWithAudit(store *credstore.Store, audit *ActivityRing) modulert.BridgeCapFactory {
	return func(mod *modulert.Module, bridge *modulert.HostBridge) modulert.CapHandler {
		return func(operation string, payload []byte) ([]byte, error) {
			switch operation {
			case "secrets.get":
				return handleSecretsGet(store, bridge, payload), nil
			case "secrets.status":
				return handleSecretsStatus(store, bridge, payload), nil
			case "secrets.put":
				return handleSecretsPut(store, bridge, audit, payload), nil
			case "secrets.clear":
				return handleSecretsClear(store, bridge, audit, payload), nil
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
// Approving "secrets:spacetrack" grants exactly the spacetrack credential.
func CapabilityForID(id string) string {
	return "secrets:" + strings.TrimSpace(id)
}

// WriteCapabilityForID is the capability name gating WRITES to one credential
// lane: "secrets:spacetrack:write".
//
// ⛔ IT IS A SEPARATE GRANT FROM THE READ CAPABILITY, DELIBERATELY.
// capability_policy.json is an append-only operator ledger. Rows approving
// "secrets:spacetrack" were signed off (2026-07-18, 2026-07-28) for READING a
// credential. If writes rode that same name, every one of those existing rows
// would silently gain the power to overwrite the operator's credentials the
// moment the binary was upgraded — retroactive privilege escalation with no
// operator act. So a write needs its own row, approved after this capability
// existed. (Seal Council, Hermes + Hephaestus, 2026-08-08.)
//
// It still begins with "secrets:", so modulert.IsSensitiveCapability gates it
// by prefix and an unapproved module is DENIED AT LOAD — fail closed, same as
// the read lane. Holding the write capability grants no read: secrets.get
// checks CapabilityForID and nothing else.
func WriteCapabilityForID(id string) string {
	return "secrets:" + strings.TrimSpace(id) + ":write"
}

// handleSecretsGet returns the credential for a requested lane.
//
//	request:  {"id":"spacetrack"}
//	response: {"username":"...","secret":"..."}
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
		return refuseCapJSON("secrets.get", fmt.Sprintf("requires the %s capability grant", capability))
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
		return refuseCapJSON("secrets.status", fmt.Sprintf("requires the %s capability grant", capability))
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

// handleSecretsPut stores a credential a module obtained itself — the browser
// hands provider credentials to the module, the module hands them to the node's
// keystore, and they are encrypted at rest under the machine-bound root exactly
// like an operator-entered credential (credstore.Store.Put, secrets.go:581).
//
//	request:  {"id":"<lane>","username":"...","secret":"..."}
//	response: {"id":"<lane>","stored":true,"updatedAt":"..."}
//
// THE RESPONSE NEVER ECHOES THE VALUE, and neither does any log line or audit
// event this path emits — the lane id and the outcome are the whole record. A
// write-only grant must not become a read oracle by reflection.
//
// Fail closed at every step: no store, no bridge, blank id, a lane id that does
// not validate, or a module lacking secrets:<lane>:write all return an error
// and change nothing on disk.
func handleSecretsPut(store *credstore.Store, bridge *modulert.HostBridge, audit *ActivityRing, payload []byte) []byte {
	var request struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Secret   string `json:"secret"`
	}
	if err := json.Unmarshal(payload, &request); err != nil {
		return errCapJSON("invalid secrets.put request payload")
	}
	id := strings.TrimSpace(request.ID)
	if id == "" {
		return errCapJSON("secrets.put requires an id")
	}

	// PER-LANE WRITE GATE. Note this checks the WRITE capability only: a module
	// approved to READ secrets:spacetrack cannot reach this line.
	capability := WriteCapabilityForID(id)
	if bridge == nil || !bridge.HasCapability(capability) {
		return refuseCapJSON("secrets.put", fmt.Sprintf("requires the %s capability grant", capability))
	}

	if store == nil {
		return errCapJSON("credential store is not available on this node")
	}

	if err := store.Put(id, request.Username, request.Secret); err != nil {
		// store.Put's errors are about the lane id and field presence, never
		// about the value, so they are safe to surface — but keep them shaped
		// rather than wrapped, so no future error string can leak material.
		return errCapJSON(fmt.Sprintf("secrets.put rejected for lane %q: %s", id, secretsWriteErrorReason(err)))
	}

	log.Infof("secrets.put stored credential lane %q (value redacted)", id)
	audit.Append("secrets_write", "", "put lane="+id)

	status, err := store.Status(id)
	if err != nil {
		// The write succeeded; only the read-back for the timestamp failed.
		return okCapJSON(map[string]interface{}{"id": id, "stored": true})
	}
	return okCapJSON(map[string]interface{}{
		"id":        id,
		"stored":    true,
		"updatedAt": status.UpdatedAt,
	})
}

// handleSecretsClear removes a lane's credential. Clearing an absent lane is
// not an error (credstore.Store.Clear, secrets.go:627), so the response says
// only that the lane is now empty — it does not disclose whether anything was
// there, which would make a write-only grant a presence oracle.
//
//	request:  {"id":"<lane>"}
//	response: {"id":"<lane>","cleared":true}
func handleSecretsClear(store *credstore.Store, bridge *modulert.HostBridge, audit *ActivityRing, payload []byte) []byte {
	var request struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(payload, &request); err != nil {
		return errCapJSON("invalid secrets.clear request payload")
	}
	id := strings.TrimSpace(request.ID)
	if id == "" {
		return errCapJSON("secrets.clear requires an id")
	}

	capability := WriteCapabilityForID(id)
	if bridge == nil || !bridge.HasCapability(capability) {
		return refuseCapJSON("secrets.clear", fmt.Sprintf("requires the %s capability grant", capability))
	}

	if store == nil {
		return errCapJSON("credential store is not available on this node")
	}

	if err := store.Clear(id); err != nil {
		return errCapJSON(fmt.Sprintf("secrets.clear failed for lane %q", id))
	}

	log.Infof("secrets.clear removed credential lane %q", id)
	audit.Append("secrets_write", "", "clear lane="+id)

	return okCapJSON(map[string]interface{}{"id": id, "cleared": true})
}

// secretsWriteErrorReason reduces a credstore write error to a fixed phrase.
// Nothing derived from the SECRET may appear in an error string.
func secretsWriteErrorReason(err error) string {
	if err == nil {
		return "unknown"
	}
	message := err.Error()
	switch {
	case strings.Contains(message, "username required"):
		return "username required"
	case strings.Contains(message, "secret required"):
		return "secret required"
	case strings.Contains(message, "lane id"):
		return "invalid lane id"
	default:
		return "credential store rejected the write"
	}
}

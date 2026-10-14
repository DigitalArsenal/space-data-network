package modulert

import "sync"

// HostSecrets are secrets the host itself holds for one module, by ID: key material the node already owns (the
// licensing runtime's content keys come from the plugin registry's bundle keys) rather than an operator credential in
// the keystore. The typed secrets.get hostcall answers an ID held here before it asks the "secrets" capability, and
// answers it as the exact bytes on raw "secret". It serves the module only when its manifest declares "secrets" and
// the operator policy approves the "secrets:<id>" lane for the module's content hash; the host code that provisions
// a secret records that approval.
type HostSecrets struct {
	mu     sync.RWMutex
	values map[string][]byte
}

// NewHostSecrets returns an empty set.
func NewHostSecrets() *HostSecrets {
	return &HostSecrets{values: make(map[string][]byte)}
}

// Set holds a copy of value under id.
func (s *HostSecrets) Set(id string, value []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[id] = append([]byte(nil), value...)
}

func (s *HostSecrets) get(id string) ([]byte, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.values[id]
	return value, ok
}

// hostSecret is the host-held secret id names, when this bridge's module may read it.
func (hb *HostBridge) hostSecret(id string) ([]byte, bool) {
	if hb == nil || hb.nodeCtx == nil || id == "" || !hb.granted["secrets"] {
		return nil, false
	}
	value, ok := hb.nodeCtx.HostSecrets.get(id)
	if !ok || !hb.nodeCtx.CapabilityPolicy.IsApproved(hb.moduleHash, SecretsCapabilityPrefix+id) {
		return nil, false
	}
	return append([]byte(nil), value...), true
}

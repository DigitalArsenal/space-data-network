package compute

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	piv "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PIV"
	thp "github.com/DigitalArsenal/spacedatastandards.org/lib/go/THP"

	"github.com/spacedatanetwork/sdn-server/internal/license"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/plugins"
)

const (
	// MaxHydrateRecords bounds store-backed input hydration per TAB port.
	MaxHydrateRecords = 4096

	payloadWireFormatAlignedBinary = 1
)

// ModuleResolver locates loaded WASM modules generically by plugin ID or method ID.
type ModuleResolver interface {
	GetModule(id string) (*modulert.Module, bool)
	FindModuleByMethod(methodID string) (string, *modulert.Module, bool)
}

// PluginManagerResolver adapts *plugins.Manager to ModuleResolver.
type PluginManagerResolver struct {
	Manager *plugins.Manager
}

// GetModule returns the loaded modulert.Module for id.
func (r PluginManagerResolver) GetModule(id string) (*modulert.Module, bool) {
	if r.Manager == nil {
		return nil, false
	}
	p := r.Manager.Get(strings.TrimSpace(id))
	if p == nil {
		return nil, false
	}
	mod, ok := p.(*modulert.Module)
	return mod, ok && mod != nil
}

// FindModuleByMethod resolves an unambiguous modulert.Module declaring methodID.
func (r PluginManagerResolver) FindModuleByMethod(methodID string) (string, *modulert.Module, bool) {
	if r.Manager == nil {
		return "", nil, false
	}
	target := strings.TrimSpace(methodID)
	if target == "" {
		return "", nil, false
	}
	var matchedID string
	var matchedMod *modulert.Module
	matches := 0
	for _, p := range r.Manager.RegisteredPlugins() {
		mod, ok := p.(*modulert.Module)
		if !ok || mod == nil {
			continue
		}
		manifest := mod.Manifest()
		if manifest == nil {
			continue
		}
		for _, m := range manifest.Methods {
			if m.MethodID == target {
				matchedID = mod.ID()
				matchedMod = mod
				matches++
				break
			}
		}
	}
	if matches != 1 {
		return "", nil, false
	}
	return matchedID, matchedMod, true
}

// InvokeMetadata describes the module that executed a compute request.
type InvokeMetadata struct {
	ModuleID   string
	MethodID   string
	WasmSHA256 string
}

// InvokeError is a structured, transport-agnostic compute error mapped to $QRP.
type InvokeError struct {
	HTTPStatus     int
	Code           string
	Message        string
	FallbackSignal string
}

func (e *InvokeError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// Service executes generic, application-blind $PIV requests on loaded WASM
// modules with fail-closed subscription entitlement enforcement.
type Service struct {
	resolver     ModuleResolver
	store        *storage.FlatSQLStore
	validator    *sds.Validator
	entitlements *license.EntitlementStore
	grantPolicy  func() *license.GrantPolicyConfig
}

// NewService constructs a generic compute offload service.
func NewService(
	resolver ModuleResolver,
	store *storage.FlatSQLStore,
	validator *sds.Validator,
	entitlements *license.EntitlementStore,
	grantPolicy func() *license.GrantPolicyConfig,
) *Service {
	return &Service{
		resolver:     resolver,
		store:        store,
		validator:    validator,
		entitlements: entitlements,
		grantPolicy:  grantPolicy,
	}
}

func (s *Service) effectiveGrantPolicy() *license.GrantPolicyConfig {
	if s != nil && s.grantPolicy != nil {
		if cfg := s.grantPolicy(); cfg != nil {
			return cfg
		}
	}
	return &license.GrantPolicyConfig{}
}

// Authorize checks whether ent is permitted to run remote compute offload.
func (s *Service) Authorize(ent *license.Entitlement) *InvokeError {
	policy := s.effectiveGrantPolicy()
	allowed, reason := policy.ResolveComputeOffloadAccess(ent, time.Now().UTC())
	if allowed {
		return nil
	}
	fallback := policy.ComputeFallbackSignal()
	return &InvokeError{
		HTTPStatus:     http.StatusPaymentRequired,
		Code:           "subscription_required",
		Message:        fmt.Sprintf("%s; fallback=%s", reason, fallback),
		FallbackSignal: fallback,
	}
}

// AuthorizeXPub resolves the caller's xpub against EntitlementStore and checks compute offload policy.
func (s *Service) AuthorizeXPub(xpub string) (*license.Entitlement, *InvokeError) {
	trimmed := strings.TrimSpace(xpub)
	if trimmed == "" || s == nil || s.entitlements == nil {
		return nil, s.Authorize(nil)
	}
	ent, err := s.entitlements.GetEntitlement(trimmed)
	if err != nil {
		return nil, s.Authorize(nil)
	}
	if invokeErr := s.Authorize(ent); invokeErr != nil {
		return ent, invokeErr
	}
	return ent, nil
}

// AuthorizePeerID resolves a libp2p peer ID against EntitlementStore and checks compute offload policy.
func (s *Service) AuthorizePeerID(peerID string) (*license.Entitlement, *InvokeError) {
	trimmed := strings.TrimSpace(peerID)
	if trimmed == "" || s == nil || s.entitlements == nil {
		return nil, s.Authorize(nil)
	}
	ent, err := s.entitlements.GetEntitlementByPeerID(trimmed)
	if err != nil {
		return nil, s.Authorize(nil)
	}
	if invokeErr := s.Authorize(ent); invokeErr != nil {
		return ent, invokeErr
	}
	return ent, nil
}

// UpsertEntitlementRecord verifies a bare or size-prefixed $ENT FlatBuffer and
// persists it into EntitlementStore.
func (s *Service) UpsertEntitlementRecord(raw []byte) (ent *license.Entitlement, invokeErr *InvokeError) {
	defer func() {
		if recovered := recover(); recovered != nil {
			invokeErr = &InvokeError{
				HTTPStatus: http.StatusBadRequest,
				Code:       "bad_request",
				Message:    fmt.Sprintf("invalid $ENT payload: %v", recovered),
			}
		}
	}()
	if s == nil || s.entitlements == nil {
		return nil, &InvokeError{
			HTTPStatus: http.StatusServiceUnavailable,
			Code:       "entitlements_unavailable",
			Message:    "entitlement store is unavailable on this node",
		}
	}
	if err := sds.VerifyStructure("ENT.fbs", raw); err != nil {
		return nil, &InvokeError{
			HTTPStatus: http.StatusBadRequest,
			Code:       "bad_request",
			Message:    fmt.Sprintf("invalid $ENT record: %v", err),
		}
	}
	decoded, err := license.DecodeEntitlementRecord(raw)
	if err != nil {
		return nil, &InvokeError{
			HTTPStatus: http.StatusBadRequest,
			Code:       "bad_request",
			Message:    err.Error(),
		}
	}
	if err := s.entitlements.UpsertEntitlement(decoded); err != nil {
		return nil, &InvokeError{
			HTTPStatus: http.StatusInternalServerError,
			Code:       "entitlement_upsert_failed",
			Message:    err.Error(),
		}
	}
	return decoded, nil
}

// Invoke verifies a $PIV envelope, enforces entitlement policy, hydrates any
// store-backed inputs, executes InvokeMethodStream on the target WASM module,
// and streams each output frame to emit.
func (s *Service) Invoke(
	ctx context.Context,
	ent *license.Entitlement,
	moduleHint string,
	pivBytes []byte,
	emit func(InvokeMetadata, modulert.InvokeOutputFrame) error,
) (meta *InvokeMetadata, invokeErr *InvokeError) {
	defer func() {
		if recovered := recover(); recovered != nil {
			invokeErr = &InvokeError{
				HTTPStatus: http.StatusBadRequest,
				Code:       "malformed_piv",
				Message:    fmt.Sprintf("invalid $PIV payload: %v\n%s", recovered, debug.Stack()),
			}
		}
	}()

	if s == nil || s.resolver == nil {
		return nil, &InvokeError{
			HTTPStatus: http.StatusServiceUnavailable,
			Code:       "compute_unavailable",
			Message:    "compute module runtime is unavailable on this node",
		}
	}

	if authErr := s.Authorize(ent); authErr != nil {
		return nil, authErr
	}

	barePIV, err := verifyAndUnwrapPIV(pivBytes)
	if err != nil {
		return nil, &InvokeError{
			HTTPStatus: http.StatusBadRequest,
			Code:       "bad_request",
			Message:    err.Error(),
		}
	}

	root := piv.GetRootAsPIV(barePIV, 0)
	req := root.Request(nil)
	if req == nil {
		return nil, &InvokeError{
			HTTPStatus: http.StatusBadRequest,
			Code:       "bad_request",
			Message:    "$PIV envelope does not contain a request",
		}
	}

	rawMethodID := strings.TrimSpace(string(req.MethodId()))
	if rawMethodID == "" {
		return nil, &InvokeError{
			HTTPStatus: http.StatusBadRequest,
			Code:       "bad_request",
			Message:    "$PIV request METHOD_ID is required",
		}
	}

	effectiveModuleID := strings.TrimSpace(moduleHint)
	effectiveMethodID := rawMethodID
	if effectiveModuleID == "" {
		if idx := strings.IndexByte(rawMethodID, ':'); idx > 0 && idx < len(rawMethodID)-1 {
			effectiveModuleID = strings.TrimSpace(rawMethodID[:idx])
			effectiveMethodID = strings.TrimSpace(rawMethodID[idx+1:])
		}
	}

	var mod *modulert.Module
	if effectiveModuleID != "" {
		var ok bool
		mod, ok = s.resolver.GetModule(effectiveModuleID)
		if !ok || mod == nil {
			return nil, &InvokeError{
				HTTPStatus: http.StatusNotFound,
				Code:       "module_not_found",
				Message:    fmt.Sprintf("compute module %q is not loaded on this node", effectiveModuleID),
			}
		}
	} else {
		resolvedID, resolvedMod, ok := s.resolver.FindModuleByMethod(effectiveMethodID)
		if !ok || resolvedMod == nil {
			return nil, &InvokeError{
				HTTPStatus: http.StatusNotFound,
				Code:       "module_not_found",
				Message:    fmt.Sprintf("no unambiguous compute module provides method %q; pass X-SDN-Module-Id", effectiveMethodID),
			}
		}
		effectiveModuleID = resolvedID
		mod = resolvedMod
	}

	inputFrames, hydrateErr := s.materializeInputFrames(req, ent)
	if hydrateErr != nil {
		return nil, hydrateErr
	}

	outputStreamCap := req.OutputStreamCap()
	meta = &InvokeMetadata{
		ModuleID:   mod.ID(),
		MethodID:   effectiveMethodID,
		WasmSHA256: mod.ContentHash(),
	}

	if err := mod.InvokeMethodStream(ctx, effectiveMethodID, inputFrames, outputStreamCap, func(frame modulert.InvokeOutputFrame) error {
		if emit == nil {
			return nil
		}
		return emit(*meta, frame)
	}); err != nil {
		return meta, &InvokeError{
			HTTPStatus: http.StatusBadGateway,
			Code:       "compute_invoke_failed",
			Message:    err.Error(),
		}
	}
	return meta, nil
}

func verifyAndUnwrapPIV(raw []byte) ([]byte, error) {
	if len(raw) < 8 {
		return nil, errors.New("request body is too short to be a $PIV FlatBuffer")
	}
	if len(raw) >= 12 && piv.PIVBufferHasIdentifier(raw[4:]) {
		frameLen := binary.LittleEndian.Uint32(raw[:4])
		if int64(frameLen) != int64(len(raw))-4 {
			return nil, fmt.Errorf("size-prefixed $PIV length mismatch: header=%d body=%d", frameLen, len(raw)-4)
		}
		if err := sds.VerifyBareStructure("PIV.fbs", raw[4:]); err != nil {
			return nil, fmt.Errorf("invalid size-prefixed $PIV: %w", err)
		}
		return raw[4:], nil
	}
	if piv.PIVBufferHasIdentifier(raw) {
		if err := sds.VerifyBareStructure("PIV.fbs", raw); err != nil {
			return nil, fmt.Errorf("invalid $PIV: %w", err)
		}
		return raw, nil
	}
	return nil, errors.New("request body is not a $PIV FlatBuffer")
}

func (s *Service) materializeInputFrames(req *piv.PIVRequest, ent *license.Entitlement) ([]modulert.InvokeInputFrame, *InvokeError) {
	inputCount := req.InputsLength()
	if inputCount == 0 {
		return nil, &InvokeError{
			HTTPStatus: http.StatusBadRequest,
			Code:       "bad_request",
			Message:    "$PIV request must contain at least one input frame",
		}
	}

	arena := req.PayloadArenaBytes()
	frames := make([]modulert.InvokeInputFrame, 0, inputCount)

	for i := 0; i < inputCount; i++ {
		var tab piv.TAB
		if !req.Inputs(&tab, i) {
			continue
		}
		portID := strings.TrimSpace(string(tab.PortId()))
		if portID == "" {
			return nil, &InvokeError{
				HTTPStatus: http.StatusBadRequest,
				Code:       "bad_request",
				Message:    fmt.Sprintf("input TAB[%d] is missing PORT_ID", i),
			}
		}

		var schemaName, fileIdentifier, rootTypeName string
		var fixedStrLen, reqAlign uint16
		var byteLen uint32
		if typeRef := tab.TypeRef(nil); typeRef != nil {
			schemaName = strings.TrimSpace(string(typeRef.SchemaName()))
			fileIdentifier = strings.TrimSpace(string(typeRef.FileIdentifier()))
			rootTypeName = strings.TrimSpace(string(typeRef.RootType()))
			fixedStrLen = typeRef.FixedStringLength()
			byteLen = typeRef.ByteLength()
			reqAlign = typeRef.RequiredAlignment()
		}
		if schemaName == "" && fileIdentifier != "" && s.validator != nil {
			if resolved, ok := s.validator.SchemaForIdentifier(fileIdentifier); ok {
				schemaName = resolved
			}
		}

		offset := tab.Offset()
		size := tab.Size()
		wireFormat := byte(tab.WireFormat())
		alignment := uint16(tab.Alignment())

		if size > 0 {
			end := uint64(offset) + uint64(size)
			if end > uint64(len(arena)) {
				return nil, &InvokeError{
					HTTPStatus: http.StatusBadRequest,
					Code:       "bad_request",
					Message:    fmt.Sprintf("input TAB[%d] (%s) exceeds PAYLOAD_ARENA bounds", i, portID),
				}
			}
			payload := append([]byte(nil), arena[offset:end]...)
			if err := s.enforceRecordTierAccess(payload, ent); err != nil {
				return nil, err
			}
			frames = append(frames, modulert.InvokeInputFrame{
				PortID:            portID,
				Payload:           payload,
				SchemaName:        schemaName,
				FileIdentifier:    fileIdentifier,
				RootTypeName:      rootTypeName,
				WireFormat:        wireFormat,
				FixedStringLength: fixedStrLen,
				ByteLength:        byteLen,
				RequiredAlignment: reqAlign,
				Alignment:         alignment,
			})
			continue
		}

		// Generic store-backed hydration when TAB.SIZE == 0 and SCHEMA_NAME is set.
		if schemaName != "" {
			if s.store == nil {
				return nil, &InvokeError{
					HTTPStatus: http.StatusServiceUnavailable,
					Code:       "storage_unavailable",
					Message:    "FlatSQL store unavailable for input hydration",
				}
			}
			records, err := s.store.QueryRawRecords(storage.RawRecordQuery{
				SchemaName: schemaName,
				Limit:      MaxHydrateRecords,
			})
			if err != nil {
				return nil, &InvokeError{
					HTTPStatus: http.StatusInternalServerError,
					Code:       "store_query_failed",
					Message:    fmt.Sprintf("hydrate %s inputs: %v", schemaName, err),
				}
			}
			for _, rec := range records {
				if rec == nil || len(rec.Data) == 0 {
					continue
				}
				if tierErr := s.enforceRecordTierAccess(rec.Data, ent); tierErr != nil {
					// Skip restricted records that the caller's tier does not admit.
					continue
				}
				frames = append(frames, modulert.InvokeInputFrame{
					PortID:            portID,
					Payload:           append([]byte(nil), rec.Data...),
					SchemaName:        schemaName,
					FileIdentifier:    fileIdentifier,
					RootTypeName:      rootTypeName,
					WireFormat:        wireFormat,
					FixedStringLength: fixedStrLen,
					ByteLength:        byteLen,
					RequiredAlignment: reqAlign,
					Alignment:         alignment,
				})
			}
		}
	}

	if len(frames) == 0 {
		return nil, &InvokeError{
			HTTPStatus: http.StatusBadRequest,
			Code:       "bad_request",
			Message:    "no usable input frames resolved for compute invocation",
		}
	}
	return frames, nil
}

func (s *Service) enforceRecordTierAccess(payload []byte, ent *license.Entitlement) *InvokeError {
	ident, ok := sds.FileIdentifierFromBuffer(payload)
	if !ok || ident != thp.THPIdentifier {
		return nil
	}
	bare := payload
	if len(payload) >= 12 && thp.SizePrefixedTHPBufferHasIdentifier(payload) {
		if err := sds.VerifySizePrefixedStructure("THP.fbs", payload); err != nil {
			return &InvokeError{
				HTTPStatus: http.StatusBadRequest,
				Code:       "bad_request",
				Message:    fmt.Sprintf("invalid $THP input: %v", err),
			}
		}
		bare = payload[4:]
	} else {
		if err := sds.VerifyBareStructure("THP.fbs", payload); err != nil {
			return &InvokeError{
				HTTPStatus: http.StatusBadRequest,
				Code:       "bad_request",
				Message:    fmt.Sprintf("invalid $THP input: %v", err),
			}
		}
	}
	profile := thp.GetRootAsTHP(bare, 0)
	tier := profile.ACCESS_TIER().String()
	allowed, reason := s.effectiveGrantPolicy().ResolveTHPAccess(tier, ent, time.Now().UTC())
	if !allowed {
		return &InvokeError{
			HTTPStatus:     http.StatusPaymentRequired,
			Code:           "subscription_required",
			Message:        reason,
			FallbackSignal: s.effectiveGrantPolicy().ComputeFallbackSignal(),
		}
	}
	return nil
}

// WriteSizePrefixedFrame writes [u32le len][payload] to dst.
func WriteSizePrefixedFrame(payload []byte) []byte {
	frame := make([]byte, 4+len(payload))
	binary.LittleEndian.PutUint32(frame[:4], uint32(len(payload)))
	copy(frame[4:], payload)
	return frame
}

// Unused import anchor guard for payloadWireFormatAlignedBinary if needed later.
var _ = payloadWireFormatAlignedBinary

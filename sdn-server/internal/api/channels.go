package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/channels"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

type ChannelHandler struct {
	store         *storage.FlatSQLStore
	gate          *channels.AccessGate
	subscriptions *channels.SubscriptionRegistry
}

func NewChannelHandler(store *storage.FlatSQLStore) *ChannelHandler {
	return &ChannelHandler{
		store:         store,
		gate:          channels.NewAccessGate(nil),
		subscriptions: channels.NewSubscriptionRegistry(),
	}
}

func (h *ChannelHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/channels", h.handleCollection)
	mux.HandleFunc("/api/v1/channels/", h.handleChannel)
}

func (h *ChannelHandler) handleCollection(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/v1/channels" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	standardFilter := strings.TrimSpace(r.URL.Query().Get("standardCode"))
	if standardFilter == "" {
		standardFilter = strings.TrimSpace(r.URL.Query().Get("standard"))
	}
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("visibility")), "private") {
		h.writeAccessDenied(w, channels.AccessDecision{
			Allowed:    false,
			GrantState: "required",
			Reason:     "verified channel grant required",
		})
		return
	}
	results := make([]map[string]interface{}, 0)
	if standardFilter != "" {
		code, err := channels.AssertStandardCode(standardFilter)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		results = append(results, channelListRow(code))
	} else {
		for _, schemaName := range sds.SupportedSchemas {
			code, err := channels.StandardCodeFromSchemaName(schemaName)
			if err != nil {
				continue
			}
			results = append(results, channelListRow(code))
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"count":   len(results),
		"results": results,
	})
}

func (h *ChannelHandler) handleChannel(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/channels/")
	rest = strings.Trim(rest, "/")
	if rest == "" {
		http.NotFound(w, r)
		return
	}

	parts := strings.Split(rest, "/")
	channelID := parts[0]
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	if len(parts) > 2 {
		http.NotFound(w, r)
		return
	}
	parsed, err := channels.ParseChannelID(channelID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	switch action {
	case "":
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, h.channelDetail(parsed))
	case "monitor":
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, h.channelMonitor(parsed))
	case "pnm":
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeError(w, http.StatusNotFound, "verified PNM unavailable for channel")
	case "subscribe":
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if h.isPrivateVisibilityRequest(r) {
			h.requireGrant(w, parsed, channels.BoundarySubscribe)
			return
		}
		writeJSON(w, http.StatusOK, h.subscriptionResponse(parsed, h.subscriptions.Subscribe(parsed)))
	case "unsubscribe":
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if h.isPrivateVisibilityRequest(r) {
			h.requireGrant(w, parsed, channels.BoundaryUnsubscribe)
			return
		}
		writeJSON(w, http.StatusOK, h.subscriptionResponse(parsed, h.subscriptions.Unsubscribe(parsed)))
	case "publish":
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.requireGrant(w, parsed, channels.BoundaryPublish)
	case "stream":
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.requireGrant(w, parsed, channels.BoundaryStreamOpen)
	case "bytes":
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.requireGrant(w, parsed, channels.BoundaryByteRangeRead)
	case "key-unwrap":
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.requireGrant(w, parsed, channels.BoundaryKeyUnwrap)
	case "shard-import":
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.requireGrant(w, parsed, channels.BoundaryShardImport)
	case "module-feed":
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.requireGrant(w, parsed, channels.BoundaryModuleFeedDelivery)
	case "cache":
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.requireGrant(w, parsed, channels.BoundaryLocalCacheRead)
	case "grants":
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.requireGrant(w, parsed, channels.BoundaryGrantIssue)
	default:
		http.NotFound(w, r)
	}
}

func (h *ChannelHandler) requireGrant(w http.ResponseWriter, parsed channels.ChannelID, boundary channels.AccessBoundary) {
	gate := h.gate
	if gate == nil {
		gate = channels.NewAccessGate(nil)
	}
	decision := gate.Authorize(channels.AccessRequest{
		Channel:  parsed,
		Boundary: boundary,
	})
	if !decision.Allowed {
		h.writeAccessDenied(w, decision)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"channelId":  parsed.ChannelID,
		"grantState": decision.GrantState,
	})
}

func (h *ChannelHandler) writeAccessDenied(w http.ResponseWriter, decision channels.AccessDecision) {
	message := decision.Reason
	if strings.TrimSpace(message) == "" {
		message = "verified channel grant required"
	}
	writeError(w, http.StatusForbidden, message)
}

func (h *ChannelHandler) isPrivateVisibilityRequest(r *http.Request) bool {
	return strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("visibility")), "private")
}

func channelListRow(standardCode string) map[string]interface{} {
	return map[string]interface{}{
		"standardCode": standardCode,
		"topic":        channels.DiscoveryTopic(standardCode),
		"visibility":   "unknown",
	}
}

func (h *ChannelHandler) channelDetail(parsed channels.ChannelID) map[string]interface{} {
	state := h.subscriptions.Get(parsed)
	return map[string]interface{}{
		"channelId":       parsed.ChannelID,
		"sourceId":        parsed.SourceID,
		"standardCode":    parsed.StandardCode,
		"feedUuid":        emptyStringAsNil(parsed.FeedUUID),
		"visibility":      state.Visibility,
		"subscribed":      state.Subscribed,
		"pnmVerified":     false,
		"grantState":      state.GrantState,
		"encryptionState": state.EncryptionState,
	}
}

func (h *ChannelHandler) channelMonitor(parsed channels.ChannelID) map[string]interface{} {
	payload := h.channelDetail(parsed)
	payload["channelHead"] = ""
	payload["providerPeer"] = ""
	payload["localRows"] = 0
	payload["remoteRows"] = 0
	payload["syncedRows"] = 0
	payload["missingRows"] = 0
	payload["pinnedRows"] = 0
	payload["syncedBytes"] = 0
	payload["throughputBytesPerSecond"] = 0
	payload["wireSpeedUtilization"] = nil
	payload["lastVerifiedUpdate"] = ""
	return payload
}

func (h *ChannelHandler) subscriptionResponse(parsed channels.ChannelID, state channels.SubscriptionState) map[string]interface{} {
	payload := h.channelDetail(parsed)
	payload["subscribed"] = state.Subscribed
	payload["visibility"] = state.Visibility
	payload["grantState"] = state.GrantState
	payload["encryptionState"] = state.EncryptionState
	if !state.UpdatedAt.IsZero() {
		payload["lastUpdated"] = state.UpdatedAt.Format(time.RFC3339Nano)
	}
	return payload
}

func emptyStringAsNil(value string) interface{} {
	if value == "" {
		return nil
	}
	return value
}

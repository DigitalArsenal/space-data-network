package api

import (
	"net/http"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/channels"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

type ChannelHandler struct {
	store *storage.FlatSQLStore
	gate  *channels.AccessGate
}

func NewChannelHandler(store *storage.FlatSQLStore) *ChannelHandler {
	return &ChannelHandler{
		store: store,
		gate:  channels.NewAccessGate(nil),
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
		writeJSON(w, http.StatusOK, channelDetail(parsed))
	case "monitor":
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, channelMonitor(parsed))
	case "pnm":
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeError(w, http.StatusNotFound, "verified PNM unavailable for channel")
	case "subscribe":
		h.requireGrant(w, parsed, channels.BoundarySubscribe)
	case "unsubscribe":
		h.requireGrant(w, parsed, channels.BoundaryUnsubscribe)
	case "publish":
		h.requireGrant(w, parsed, channels.BoundaryPublish)
	case "stream":
		h.requireGrant(w, parsed, channels.BoundaryStreamOpen)
	case "bytes":
		h.requireGrant(w, parsed, channels.BoundaryByteRangeRead)
	case "key-unwrap":
		h.requireGrant(w, parsed, channels.BoundaryKeyUnwrap)
	case "shard-import":
		h.requireGrant(w, parsed, channels.BoundaryShardImport)
	case "module-feed":
		h.requireGrant(w, parsed, channels.BoundaryModuleFeedDelivery)
	case "cache":
		h.requireGrant(w, parsed, channels.BoundaryLocalCacheRead)
	case "grants":
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

func channelListRow(standardCode string) map[string]interface{} {
	return map[string]interface{}{
		"standardCode": standardCode,
		"topic":        channels.DiscoveryTopic(standardCode),
		"visibility":   "unknown",
	}
}

func channelDetail(parsed channels.ChannelID) map[string]interface{} {
	return map[string]interface{}{
		"channelId":       parsed.ChannelID,
		"sourceId":        parsed.SourceID,
		"standardCode":    parsed.StandardCode,
		"feedUuid":        emptyStringAsNil(parsed.FeedUUID),
		"visibility":      "unknown",
		"pnmVerified":     false,
		"grantState":      "unknown",
		"encryptionState": "unknown",
	}
}

func channelMonitor(parsed channels.ChannelID) map[string]interface{} {
	payload := channelDetail(parsed)
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

func emptyStringAsNil(value string) interface{} {
	if value == "" {
		return nil
	}
	return value
}

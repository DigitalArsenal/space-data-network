package api

// The node pin policy lane ($PIN), operator only.
//
//	GET  /api/node/pins  the whole policy: one size-prefixed $PIN, every rule
//	                     with the roots and bytes it holds pinned.
//	POST /api/node/pins  one size-prefixed $PIN carrying the rules to change
//	                     (CLASS, FEED, ENABLED); the node applies them, pins or
//	                     unpins what they cover, and answers the whole policy.
//
// Both use the sync lane's transport: application/vnd.sdn.flatbuffers.stream,
// X-SDN-Schema: PIN.fbs; every refusal is one $QRP Error frame.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/pinpolicy"
)

const (
	// NodePinsPath is the pin policy lane.
	NodePinsPath = "/api/node/pins"
	// PinSchemaName is the store form of the $PIN standard.
	PinSchemaName = "PIN.fbs"

	pinIdentifier          = "$PIN"
	pinRequestFrameBytes   = 1 << 20
	pinApplyRequestTimeout = 10 * time.Minute
)

func init() {
	RegisterAdminMount("pins", mountNodePins)
}

func mountNodePins(mux *http.ServeMux, deps *AdminMountDeps) {
	mux.HandleFunc(NodePinsPath, deps.adminGate(nodePinsHandler(deps)))
}

func nodePinsHandler(deps *AdminMountDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || deps.Pins == nil {
			WriteErrorFrame(w, http.StatusServiceUnavailable, "unavailable", "This node has no pin policy.", 0)
			return
		}
		switch r.Method {
		case http.MethodGet:
			writePinPolicy(w, deps.Pins.Encode())
		case http.MethodPost:
			if !strings.HasPrefix(strings.TrimSpace(r.Header.Get("Content-Type")), StreamContentType) {
				WriteErrorFrame(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Send one $PIN frame as "+StreamContentType+".", 0)
				return
			}
			frames, err := ReadFrames(r.Body, pinRequestFrameBytes)
			if err != nil || len(frames) != 1 || FrameIdentifier(frames[0]) != pinIdentifier {
				WriteErrorFrame(w, http.StatusBadRequest, "bad_request", "The request body must be exactly one $PIN frame.", 0)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), pinApplyRequestTimeout)
			defer cancel()
			answer, err := deps.Pins.Apply(ctx, frames[0])
			switch {
			case errors.Is(err, pinpolicy.ErrLocked):
				WriteErrorFrame(w, http.StatusBadRequest, "locked", "The node's own EPM and its account EPMs are always pinned.", 0)
			case errors.Is(err, pinpolicy.ErrBadFrame):
				WriteErrorFrame(w, http.StatusBadRequest, "bad_request", "The request body must be exactly one $PIN frame.", 0)
			case err != nil:
				log.Warnf("pin policy change failed: %v", err)
				WriteErrorFrame(w, http.StatusInternalServerError, "internal", "The pin policy could not be saved.", 0)
			default:
				writePinPolicy(w, answer)
			}
		default:
			WriteErrorFrame(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use GET to read the pin policy or POST to change it.", 0)
		}
	}
}

func writePinPolicy(w http.ResponseWriter, frame []byte) {
	WriteFrameStream(w, http.StatusOK, [][]byte{frame}, map[string]string{StreamSchemaHeader: PinSchemaName, "Cache-Control": "no-store"})
}

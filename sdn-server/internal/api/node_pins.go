package api

// The node pin policy lane ($PIN), operator only.
//
//	GET  /api/node/pins  the whole policy: one size-prefixed $PIN, every rule
//	                     with the roots and bytes it holds pinned, then one
//	                     $PNM per document the node pins whatever the rules
//	                     say (the SDN README: name, CID, /ipfs/ path).
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

	PNM "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"
	flatbuffers "github.com/google/flatbuffers/go"

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
	// pinDocumentCheckTimeout bounds the kept-document check a read makes.
	pinDocumentCheckTimeout = 3 * time.Second
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
			// The page that reads this shows the truth: a kept document
			// someone unpinned is pinned again before the answer.
			checkCtx, cancel := context.WithTimeout(r.Context(), pinDocumentCheckTimeout)
			deps.Pins.CheckDocuments(checkCtx)
			cancel()
			writePinPolicy(w, deps.Pins.Encode(), keptDocumentFrames(deps.Pins.Documents())...)
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
				writePinPolicy(w, answer, keptDocumentFrames(deps.Pins.Documents())...)
			}
		default:
			WriteErrorFrame(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use GET to read the pin policy or POST to change it.", 0)
		}
	}
}

// writePinPolicy answers the policy's $PIN, then one $PNM per document the
// node keeps pinned whatever the rules say (the SDN README): the file's name,
// its CID and its gateway path, as a publication notification carries them.
// X-SDN-Schema names the first frame's standard.
func writePinPolicy(w http.ResponseWriter, frame []byte, documents ...[]byte) {
	frames := append([][]byte{frame}, documents...)
	WriteFrameStream(w, http.StatusOK, frames, map[string]string{StreamSchemaHeader: PinSchemaName, "Cache-Control": "no-store"})
}

// keptDocumentFrames is one size-prefixed $PNM per kept document that has a
// CID. PUBLISH_TIMESTAMP is when the node last confirmed the pin. The frame
// is not signed: it reports what this node holds, it announces nothing.
func keptDocumentFrames(docs []pinpolicy.KeptDocument) [][]byte {
	var frames [][]byte
	for _, doc := range docs {
		if doc.CID == "" || !doc.Pinned {
			continue
		}
		b := flatbuffers.NewBuilder(256)
		address := b.CreateString("/ipfs/" + doc.CID)
		checked := b.CreateString(doc.CheckedAt.UTC().Format(time.RFC3339))
		cid := b.CreateString(doc.CID)
		name := b.CreateString(doc.Name)
		PNM.PNMStart(b)
		PNM.PNMAddMULTIFORMAT_ADDRESS(b, address)
		PNM.PNMAddPUBLISH_TIMESTAMP(b, checked)
		PNM.PNMAddCID(b, cid)
		PNM.PNMAddFILE_NAME(b, name)
		PNM.FinishSizePrefixedPNMBuffer(b, PNM.PNMEnd(b))
		frames = append(frames, b.FinishedBytes())
	}
	return frames
}

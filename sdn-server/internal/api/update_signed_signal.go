package api

import (
	"io"
	"net/http"
	"os"

	"github.com/spacedatanetwork/sdn-server/internal/bundle"
	"github.com/spacedatanetwork/sdn-server/internal/update"
)

// handleSignedUpdateSignal relays the pipeline's document without using a node signing key.
func (h *CoreAPIHandler) handleSignedUpdateSignal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeCoreAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "signed signals require POST")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, update.SignalMaxBytes+1))
	if err != nil || len(raw) > update.SignalMaxBytes {
		writeCoreAPIError(w, http.StatusBadRequest, "INVALID_SIGNAL", "signal body exceeds its size limit or cannot be read")
		return
	}
	signal, err := update.ParseSignal(raw)
	if err != nil {
		writeCoreAPIError(w, http.StatusBadRequest, "INVALID_SIGNAL", err.Error())
		return
	}
	roots, err := update.LoadTrustRoots(update.PathsFor(bundle.ResolveCurrent().Root))
	if err == nil {
		err = signal.Verify(update.SignalVerifyOptions{TrustedRoots: roots})
	}
	if err != nil {
		writeCoreAPIError(w, http.StatusBadRequest, "INVALID_SIGNAL", err.Error())
		return
	}
	files, err := update.FeedSignalFiles(os.Getenv(updateFeedDirEnv), signal.Channel, signal.Version)
	if err != nil {
		writeCoreAPIError(w, http.StatusBadRequest, "FEED_UNAVAILABLE", err.Error())
		return
	}
	matched := false
	for _, file := range files {
		if file.Entry.UpdateID == signal.UpdateID && update.MatchFeedSignal(signal, file) == nil {
			matched = true
			break
		}
	}
	if !matched {
		writeCoreAPIError(w, http.StatusBadRequest, "SIGNAL_NOT_IN_FEED", "signal does not match an entry in the served feed")
		return
	}
	publisher, ok := h.publisher.(topicPublisher)
	if !ok {
		writeCoreAPIError(w, http.StatusServiceUnavailable, "PUBLISHER_UNAVAILABLE", "pubsub unavailable")
		return
	}
	topic := update.SignalTopic(signal.Channel)
	if err := publisher.PublishToTopic(r.Context(), topic, raw); err != nil {
		writeCoreAPIError(w, http.StatusServiceUnavailable, "PUBLISH_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"published": true, "topic": topic, "update_id": signal.UpdateID})
}

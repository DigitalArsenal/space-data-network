package pubsub

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"
)

// The opt-in observation file gives real-daemon benchmarks the actual start of
// successful announcements. It contains no record bytes or private key material.
// With the variable unset this is the ordinary publication call.
func publishObservedDatasetAnnouncement(publisher SchemaPublisher, schema string, frame []byte) error {
	path := os.Getenv("SDN_E2E_PUBLICATION_OBSERVATIONS")
	if path == "" {
		return publisher.Publish(schema, frame)
	}
	started := time.Now().UnixNano()
	if err := publisher.Publish(schema, frame); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("publication observation: %w", err)
	}
	event := struct {
		Started     int64  `json:"started_unix_nano"`
		Schema      string `json:"schema"`
		ManifestCID string `json:"manifest_cid"`
	}{started, schema, string(PNM.GetSizePrefixedRootAsPNM(frame, 0).CID())}
	err = json.NewEncoder(file).Encode(event)
	closeErr := file.Close()
	if err != nil {
		return fmt.Errorf("publication observation: %w", err)
	}
	return closeErr
}

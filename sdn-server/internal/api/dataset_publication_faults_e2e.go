//go:build sdn_e2e_follow_faults

package api

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A build with faults in its dataset publications, for the publication
// real-daemon tests; no release build carries this tag.
//   - A lane whose source name starts "crash-": the process exits (status 3)
//     as soon as a publication run starts, as a publication that runs the
//     node out of memory ends it.
//   - A lane whose source name starts "lateredo-": the mark of batch B the
//     previous process left is handled once the gate "redo-B" is open, so
//     the test can publish the lane meanwhile.
//   - A batch whose name starts "heldlane-": its publication, once it holds
//     its lane, waits until the gate "lane-<batch>" is open.
//
// A gate is open when a file of its name exists in the folder
// SDN_E2E_PUBLICATION_GATES names. With no folder set, nothing waits. A gate
// that is not opened within publicationGateBound is passed, logged.
func init() {
	publicationRunFault = func(req DatasetPublicationRequest) {
		if strings.HasPrefix(req.SourceName, "crash-") {
			fmt.Fprintf(os.Stderr, "test build: the publication of %s batch %q ends the process\n", datasetPublicationLaneName(req), req.BatchID)
			os.Exit(3)
		}
	}
	publicationRedoGate = func(ctx context.Context, req DatasetPublicationRequest) bool {
		if !strings.HasPrefix(req.SourceName, "lateredo-") {
			return true
		}
		return publicationGate(ctx, "redo-"+req.BatchID, fmt.Sprintf("the mark of %s batch %q is handled", datasetPublicationLaneName(req), req.BatchID))
	}
	publicationLaneHeld = func(ctx context.Context, req DatasetPublicationRequest) {
		if strings.HasPrefix(req.BatchID, "heldlane-") {
			publicationGate(ctx, "lane-"+req.BatchID, fmt.Sprintf("the publication of %s batch %q holds its lane", datasetPublicationLaneName(req), req.BatchID))
		}
	}
}

// publicationGateBound bounds the wait for a gate.
const publicationGateBound = 15 * time.Minute

// publicationGate waits until the gate name is open, logging what waits for
// it. It returns false when ctx ended first.
func publicationGate(ctx context.Context, name, what string) bool {
	dir := os.Getenv("SDN_E2E_PUBLICATION_GATES")
	if dir == "" {
		return true
	}
	path := filepath.Join(dir, name)
	log.Warnf("test build: %s until gate %q is open", what, name)
	deadline := time.Now().Add(publicationGateBound)
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			log.Warnf("test build: gate %q is open", name)
			return true
		}
		if time.Now().After(deadline) {
			log.Warnf("test build: gate %q was not opened within %s; passed", name, publicationGateBound)
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-tick.C:
		}
	}
}

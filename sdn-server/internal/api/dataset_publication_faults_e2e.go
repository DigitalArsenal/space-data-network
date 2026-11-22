//go:build sdn_e2e_follow_faults

package api

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// A build with faults in its dataset publications, for the publication
// real-daemon tests; no release build carries this tag. By the source name of
// the publication's lane:
//   - "crash-": the process exits (status 3) as soon as a publication run
//     starts, as a publication that runs the node out of memory ends it;
//   - "lateredo-": the mark the previous process left is handled 60 s after
//     the node is ready, so the test can publish the lane meanwhile.
func init() {
	publicationRunFault = func(req DatasetPublicationRequest) {
		if strings.HasPrefix(req.SourceName, "crash-") {
			fmt.Fprintf(os.Stderr, "test build: the publication of %s batch %q ends the process\n", datasetPublicationLaneName(req), req.BatchID)
			os.Exit(3)
		}
	}
	publicationRedoDelay = func(sourceName string) time.Duration {
		if strings.HasPrefix(sourceName, "lateredo-") {
			return 60 * time.Second
		}
		return 0
	}
}

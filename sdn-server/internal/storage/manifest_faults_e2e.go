//go:build sdn_e2e_follow_faults

package storage

import "strings"

// A build whose publications misstate their sizes, for the publication
// real-daemon tests; no release build carries this tag. By the source name
// of the publication's first source batch:
//   - "overstate-": the DPM states a shard of 3,689,348,814,741,910,324
//     bytes (times 5 that wraps an int64 to 4);
//   - "understate-bytes-": the DPM states half the shard's bytes;
//   - "understate-records-": the DPM states one record fewer than the shard
//     holds.
func init() {
	datasetManifestMisstate = func(export *DatasetExport) {
		if len(export.SourceBatches) == 0 {
			return
		}
		switch source := export.SourceBatches[0].SourceName; {
		case strings.HasPrefix(source, "overstate-"):
			export.ShardBytes = 3689348814741910324
		case strings.HasPrefix(source, "understate-bytes-"):
			export.ShardBytes /= 2
		case strings.HasPrefix(source, "understate-records-"):
			if export.SourceBatches[0].RecordCount > 0 {
				export.SourceBatches[0].RecordCount--
			}
		}
	}
}

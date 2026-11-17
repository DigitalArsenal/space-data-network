package storage

import "time"

// The bounds of one dataset publication of this node (internal/api). The
// node's own publish route answers within their sum, so a caller of that
// route (a flow's http-request capability) waits that long for the answer.
const (
	// DatasetPublicationLaneWait bounds how long a publication waits for the
	// publication of its lane that runs before it.
	DatasetPublicationLaneWait = time.Hour
	// DatasetPublicationDeadline bounds one publication from the moment it
	// holds its lane.
	DatasetPublicationDeadline = time.Hour
)

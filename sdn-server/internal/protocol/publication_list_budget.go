package protocol

import (
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/readbudget"
)

const (
	publicationListRows  = 1000
	publicationListBytes = 1 << 20
	// The console can make 86 first-minute reads with paginated SOURCES,
	// eleven regime catalogs, optional lanes and catalog retries (stack #643).
	publicationListReadsPerMinute = 120
	publicationListBurst          = 256
)

func newPublicationListBudget() *readbudget.Budget {
	return readbudget.New(time.Minute/publicationListReadsPerMinute, publicationListBurst)
}

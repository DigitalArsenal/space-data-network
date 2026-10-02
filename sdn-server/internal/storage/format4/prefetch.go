package format4

import (
	"os"
	"path/filepath"
)

// prefetchBudget bounds the bytes prefetchIndexes reads; prefetchChunk is
// its read size.
const (
	prefetchBudget = 2 << 30
	prefetchChunk  = 1 << 20
)

// prefetchIndexes reads every type index (fsql4/T/*.idx) once, start to end,
// so an opened store's first lookups (a GET's CID, an index page's walk, a
// window's offset) find their pages in the OS page cache instead of reading
// them one at a time from the disk: on a fresh fixture clone a GET of a CID
// not yet looked up took 0.3-0.5 ms, 0.02 ms once its page was cached.
// Sequential reads run at disk bandwidth (the fixture's 242 MB of indexes in
// 0.06 s). The pages land in the OS cache, which reclaims them, not in the
// heap. Open starts it beside the engine's start and does not wait for it
// (a fixture's indexes are read long before the engine is up; a large store
// on a slow disk does not delay the open); Close stops it.
func prefetchIndexes(engineRoot string, stop <-chan struct{}) {
	files, err := filepath.Glob(filepath.Join(engineRoot, "T", "*.idx"))
	if err != nil {
		return
	}
	buf := make([]byte, prefetchChunk)
	var total int64
	for _, name := range files {
		f, err := os.Open(name)
		if err != nil {
			continue
		}
		for total < prefetchBudget {
			select {
			case <-stop:
				_ = f.Close()
				return
			default:
			}
			n, err := f.Read(buf)
			total += int64(n)
			if err != nil {
				break
			}
		}
		_ = f.Close()
		if total >= prefetchBudget {
			return
		}
	}
}

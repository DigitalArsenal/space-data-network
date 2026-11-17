package protocol

import (
	"strings"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/datasync"
)

// FollowLane names a lane followers read: the standard (store form) and the
// provider and source the followed read names.
type FollowLane struct {
	Schema, ProviderID, SourceName string
}

func followLaneKey(q datasync.QueryRequest) FollowLane {
	return FollowLane{Schema: datasync.NormalizeSchema(q), ProviderID: strings.TrimSpace(q.ProviderID), SourceName: strings.TrimSpace(q.SourceName)}
}

// FollowStat is what this node has served to followers of one lane since it
// started: followers now, record bytes and records sent, and the rate of the
// last followServeWindow.
type FollowStat struct {
	Subscribers         int
	ServedBytes         int64
	ServedRecords       int64
	ServeBytesPerSecond int64
}

// followServeWindow is the span the serve rate is measured over.
const followServeWindow = 10 * time.Second

type followSample struct {
	at    time.Time
	bytes int64
}

type followLaneStats struct {
	FollowStat
	samples []followSample
}

// followStats counts followed reads per lane.
type followStats struct {
	mu    sync.Mutex
	lanes map[FollowLane]*followLaneStats
	now   func() time.Time
}

func newFollowStats() *followStats {
	return &followStats{lanes: map[FollowLane]*followLaneStats{}, now: time.Now}
}

func (f *followStats) lane(l FollowLane) *followLaneStats {
	s := f.lanes[l]
	if s == nil {
		s = &followLaneStats{}
		f.lanes[l] = s
	}
	return s
}

func (f *followStats) open(l FollowLane) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lane(l).Subscribers++
}

func (f *followStats) close(l FollowLane) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lane(l).Subscribers--
}

func (f *followStats) served(l FollowLane, bytes int64, records int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.lane(l)
	s.ServedBytes += bytes
	s.ServedRecords += int64(records)
	now := f.now()
	s.samples = append(s.samples, followSample{at: now, bytes: bytes})
	s.trim(now)
}

func (s *followLaneStats) trim(now time.Time) {
	keep := 0
	for keep < len(s.samples) && now.Sub(s.samples[keep].at) > followServeWindow {
		keep++
	}
	s.samples = s.samples[keep:]
}

// Snapshot is every lane's counts as of now.
func (f *followStats) Snapshot() map[FollowLane]FollowStat {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	out := make(map[FollowLane]FollowStat, len(f.lanes))
	for l, s := range f.lanes {
		s.trim(now)
		var bytes int64
		for _, sample := range s.samples {
			bytes += sample.bytes
		}
		stat := s.FollowStat
		stat.ServeBytesPerSecond = int64(float64(bytes) / followServeWindow.Seconds())
		out[l] = stat
	}
	return out
}

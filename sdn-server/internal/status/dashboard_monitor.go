package status

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultDashboardWindow is the rolling source-count window carried by
	// $NDS. The prior window has the same width immediately before it.
	DefaultDashboardWindow = 5 * time.Minute
	// MinimumDashboardStall prevents bursty, high-rate sources from being
	// declared stalled after only a few quiet polling intervals.
	MinimumDashboardStall = 10 * time.Minute
	dashboardEventLimit   = 128
	dashboardArrivalLimit = 64
)

type dashboardIngestPoint struct {
	At    int64
	Count int64
}

type dashboardSourceActivity struct {
	initialized bool
	lastCount   int64
	points      []dashboardIngestPoint
	arrivals    []int64
	stalled     bool
}

// DashboardMonitor turns successive cached source snapshots into rolling
// counters and transition events. It never guesses an initial window count:
// the first aggregate observation establishes a baseline unless exact
// IngestTimestamps are supplied. Later observations record only non-negative
// count deltas, so deletions/rebuilds cannot inflate a window.
type DashboardMonitor struct {
	mu      sync.Mutex
	window  time.Duration
	sources map[string]*dashboardSourceActivity
	events  []DashboardIngestEventRow
}

// NewDashboardMonitor creates an isolated monitor. A non-positive window uses
// the five-minute product default.
func NewDashboardMonitor(window time.Duration) *DashboardMonitor {
	if window <= 0 {
		window = DefaultDashboardWindow
	}
	return &DashboardMonitor{
		window:  window,
		sources: make(map[string]*dashboardSourceActivity),
	}
}

var defaultDashboardMonitor = NewDashboardMonitor(DefaultDashboardWindow)

// Build observes this snapshot, derives window/event/topic fields, and emits
// one size-prefixed $NDS frame. It is safe for concurrent callers.
func (m *DashboardMonitor) Build(in DashboardStatsInput) []byte {
	if m == nil {
		m = NewDashboardMonitor(DefaultDashboardWindow)
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	in.Now = now

	m.mu.Lock()
	derivedSources := append([]DashboardSourceRow(nil), in.Sources...)
	for i := range derivedSources {
		m.observeSource(&derivedSources[i], now)
	}
	in.Sources = derivedSources
	in.Events = append(append([]DashboardIngestEventRow(nil), m.events...), in.Events...)
	in.Topics = deriveDashboardTopics(in.Topics, now)
	m.mu.Unlock()

	return buildDashboardStatsSet(in)
}

func (m *DashboardMonitor) observeSource(row *DashboardSourceRow, now time.Time) {
	key := dashboardSourceKey(*row)
	activity := m.sources[key]
	if activity == nil {
		activity = &dashboardSourceActivity{}
		m.sources[key] = activity
	}

	count := row.RecordCount
	if count < 0 {
		count = 0
	}
	newRecords := int64(0)
	if !activity.initialized {
		activity.initialized = true
		activity.lastCount = count
		seedSourceHistory(activity, *row)
		if len(row.IngestTimestamps) > 0 {
			newRecords = count
		}
	} else {
		if count > activity.lastCount {
			newRecords = count - activity.lastCount
		}
		// A count decrease is a deletion/rebuild, not negative ingestion. Reset
		// the baseline and preserve the historical arrival points.
		activity.lastCount = count
	}

	latestArrival := int64(0)
	if newRecords > 0 {
		latestArrival = m.recordArrivals(activity, row.IngestTimestamps, newRecords, row.LastIngestAt, now.Unix())
		if activity.stalled {
			m.appendEvent(DashboardIngestEventRow{
				Kind:       DashboardIngestEventRecover,
				Schema:     row.Schema,
				ProviderID: row.ProviderID,
				SourceName: row.SourceName,
				Message:    fmt.Sprintf("%s resumed delivering %s records.", dashboardSourceLabel(*row), dashboardSchemaLabel(row.Schema)),
				Count:      count,
				At:         latestArrival,
			})
			activity.stalled = false
		}
	}

	m.prunePoints(activity, now.Unix())
	row.WindowRecords, row.PriorWindowRecords = m.windowCounts(activity, now.Unix())
	row.WindowMS = int64(m.window / time.Millisecond)

	if !activity.stalled && sourceIsStalled(activity, now.Unix()) {
		m.appendEvent(DashboardIngestEventRow{
			Kind:       DashboardIngestEventStall,
			Schema:     row.Schema,
			ProviderID: row.ProviderID,
			SourceName: row.SourceName,
			Message:    fmt.Sprintf("%s has stopped delivering %s records within its expected interval.", dashboardSourceLabel(*row), dashboardSchemaLabel(row.Schema)),
			Count:      count,
			At:         now.Unix(),
		})
		activity.stalled = true
	}
}

func seedSourceHistory(activity *dashboardSourceActivity, row DashboardSourceRow) {
	if len(row.IngestTimestamps) > 0 {
		return
	}
	first, last := row.FirstIngestAt, row.LastIngestAt
	if first > 0 {
		appendArrivalTime(activity, first)
	}
	if last > first {
		appendArrivalTime(activity, last)
	}
}

func (m *DashboardMonitor) recordArrivals(activity *dashboardSourceActivity, timestamps []int64, count, fallback, now int64) int64 {
	exact := append([]int64(nil), timestamps...)
	sort.Slice(exact, func(i, j int) bool { return exact[i] < exact[j] })
	filtered := exact[:0]
	for _, at := range exact {
		if at > 0 && at <= now {
			filtered = append(filtered, at)
		}
	}
	exact = filtered
	if int64(len(exact)) > count {
		exact = exact[len(exact)-int(count):]
	}

	latest := int64(0)
	for _, at := range exact {
		appendIngestPoint(activity, at, 1)
		appendArrivalTime(activity, at)
		latest = at
	}
	remaining := count - int64(len(exact))
	if remaining > 0 {
		at := fallback
		if at <= 0 || at > now {
			at = now
		}
		appendIngestPoint(activity, at, remaining)
		appendArrivalTime(activity, at)
		if at > latest {
			latest = at
		}
	}
	if latest == 0 {
		latest = now
	}
	return latest
}

func appendIngestPoint(activity *dashboardSourceActivity, at, count int64) {
	if count <= 0 || at <= 0 {
		return
	}
	index := sort.Search(len(activity.points), func(i int) bool {
		return activity.points[i].At >= at
	})
	if index < len(activity.points) && activity.points[index].At == at {
		activity.points[index].Count += count
		return
	}
	activity.points = append(activity.points, dashboardIngestPoint{})
	copy(activity.points[index+1:], activity.points[index:])
	activity.points[index] = dashboardIngestPoint{At: at, Count: count}
}

func appendArrivalTime(activity *dashboardSourceActivity, at int64) {
	if at <= 0 {
		return
	}
	n := len(activity.arrivals)
	if n > 0 && at <= activity.arrivals[n-1] {
		if at == activity.arrivals[n-1] {
			return
		}
		activity.arrivals = append(activity.arrivals, at)
		sort.Slice(activity.arrivals, func(i, j int) bool { return activity.arrivals[i] < activity.arrivals[j] })
	} else {
		activity.arrivals = append(activity.arrivals, at)
	}
	if len(activity.arrivals) > dashboardArrivalLimit {
		activity.arrivals = append([]int64(nil), activity.arrivals[len(activity.arrivals)-dashboardArrivalLimit:]...)
	}
}

func (m *DashboardMonitor) prunePoints(activity *dashboardSourceActivity, now int64) {
	oldest := now - int64((2*m.window)/time.Second)
	first := 0
	for first < len(activity.points) && activity.points[first].At <= oldest {
		first++
	}
	if first > 0 {
		activity.points = append([]dashboardIngestPoint(nil), activity.points[first:]...)
	}
}

func (m *DashboardMonitor) windowCounts(activity *dashboardSourceActivity, now int64) (current, prior int64) {
	windowSeconds := int64(m.window / time.Second)
	currentStart := now - windowSeconds
	priorStart := currentStart - windowSeconds
	for _, point := range activity.points {
		switch {
		case point.At > currentStart && point.At <= now:
			current += point.Count
		case point.At > priorStart && point.At <= currentStart:
			prior += point.Count
		}
	}
	return current, prior
}

func sourceIsStalled(activity *dashboardSourceActivity, now int64) bool {
	if len(activity.arrivals) < 2 {
		return false
	}
	intervals := make([]int64, 0, len(activity.arrivals)-1)
	for i := 1; i < len(activity.arrivals); i++ {
		if delta := activity.arrivals[i] - activity.arrivals[i-1]; delta > 0 {
			intervals = append(intervals, delta)
		}
	}
	if len(intervals) == 0 {
		return false
	}
	sort.Slice(intervals, func(i, j int) bool { return intervals[i] < intervals[j] })
	median := intervals[len(intervals)/2]
	if len(intervals)%2 == 0 {
		median = (intervals[len(intervals)/2-1] + intervals[len(intervals)/2]) / 2
	}
	threshold := 3 * median
	minimum := int64(MinimumDashboardStall / time.Second)
	if threshold < minimum {
		threshold = minimum
	}
	last := activity.arrivals[len(activity.arrivals)-1]
	return now-last > threshold
}

func (m *DashboardMonitor) appendEvent(event DashboardIngestEventRow) {
	m.events = append(m.events, event)
	if len(m.events) > dashboardEventLimit {
		m.events = append([]DashboardIngestEventRow(nil), m.events[len(m.events)-dashboardEventLimit:]...)
	}
}

func deriveDashboardTopics(rows []DashboardTopicRow, now time.Time) []DashboardTopicRow {
	out := append([]DashboardTopicRow(nil), rows...)
	cutoff := now.Unix() - 60
	for i := range out {
		if len(out[i].MessageTimestamps) == 0 {
			continue
		}
		count := 0
		latest := out[i].LastSeenAt
		for _, at := range out[i].MessageTimestamps {
			if at > latest && at <= now.Unix() {
				latest = at
			}
			if at > cutoff && at <= now.Unix() {
				count++
			}
		}
		out[i].RatePerMin = float64(count)
		out[i].LastSeenAt = latest
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Topic < out[j].Topic })
	return out
}

func dashboardSourceKey(row DashboardSourceRow) string {
	return strings.Join([]string{row.Schema, row.ProviderID, row.SourceName, row.BatchID}, "\x00")
}

func dashboardSourceLabel(row DashboardSourceRow) string {
	if value := strings.TrimSpace(row.SourceName); value != "" {
		return value
	}
	if value := strings.TrimSpace(row.ProviderID); value != "" {
		return value
	}
	return "This source"
}

func dashboardSchemaLabel(schema string) string {
	if value := strings.TrimSpace(schema); value != "" {
		return value
	}
	return "data"
}

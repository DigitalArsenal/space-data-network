package protocol

// Topic activity — what the dashboard's $NDS TOPICS lane reports. Every
// pubsub message the node admits is observed here by schema topic; the status
// frame reads the last minute's timestamps per topic. Before this existed the
// lane was never fed: the dashboard showed TOPICS 0 and every stream's rate as
// "—" while peers were delivering records (walkthrough 2026-09-02).

import (
	"sort"
	"sync"
	"time"
)

// topicActivityWindow bounds how far back observations are kept.
const topicActivityWindow = time.Minute

// topicActivityMaxPerTopic bounds memory per topic (a hot topic at 100 msg/s
// keeps its last minute; older observations are dropped first).
const topicActivityMaxPerTopic = 6000

// TopicActivity records message arrival times per topic.
type TopicActivity struct {
	mu     sync.Mutex
	topics map[string][]int64 // unix ms, ascending
}

// DefaultTopicActivity is the process-wide observer the exchange handler feeds
// and the dashboard stats lane reads.
var DefaultTopicActivity = NewTopicActivity()

// NewTopicActivity returns an empty observer.
func NewTopicActivity() *TopicActivity {
	return &TopicActivity{topics: map[string][]int64{}}
}

// Observe records one message on topic at now.
func (a *TopicActivity) Observe(topic string, now time.Time) {
	if a == nil || topic == "" {
		return
	}
	ms := now.UnixMilli()
	a.mu.Lock()
	defer a.mu.Unlock()
	ts := append(a.topics[topic], ms)
	if len(ts) > topicActivityMaxPerTopic {
		ts = ts[len(ts)-topicActivityMaxPerTopic:]
	}
	a.topics[topic] = ts
}

// TopicObservation is one topic's observations inside the window.
type TopicObservation struct {
	Topic             string
	MessageTimestamps []int64 // unix ms, ascending
}

// Snapshot returns every topic with at least one observation inside the window
// ending at now, sorted by topic. Observations older than the window are
// forgotten.
func (a *TopicActivity) Snapshot(now time.Time) []TopicObservation {
	if a == nil {
		return nil
	}
	cutoff := now.Add(-topicActivityWindow).UnixMilli()
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]TopicObservation, 0, len(a.topics))
	for topic, ts := range a.topics {
		i := sort.Search(len(ts), func(i int) bool { return ts[i] >= cutoff })
		if i > 0 {
			ts = append([]int64(nil), ts[i:]...)
			a.topics[topic] = ts
		}
		if len(ts) == 0 {
			delete(a.topics, topic)
			continue
		}
		out = append(out, TopicObservation{Topic: topic, MessageTimestamps: append([]int64(nil), ts...)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Topic < out[j].Topic })
	return out
}

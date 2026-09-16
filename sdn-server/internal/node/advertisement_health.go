package node

import (
	"sync"
	"time"
)

// sdnAdvertiseTimeout bounds one rendezvous Provide.
//
// It was 10s, and that is why nodes were never findable: a Provide on the
// public Amino DHT walks to the ~20 peers closest to the key and stores the
// record on each, which is tens of seconds on a real network. Measured on a
// publicly reachable node with a populated routing table: every attempt failed
// with "context deadline exceeded".
//
// 90s is chosen to exceed a slow walk rather than to be generous: a Provide
// that has not finished by then is not going to, and the next tick will try
// again. Kubo bounds its own provides in the same order of magnitude.
const sdnAdvertiseTimeout = 90 * time.Second

// advertisementHealth is the answer to "can anyone find this node?", kept so it
// can be logged on transitions and served over the status API instead of living
// only in a Debugf nobody enables.
//
// The failure it exists for is real and was invisible: on a fresh node the SDN
// rendezvous announce fails with "failed to find any peer in table", because it
// fires before the DHT routing table has anyone in it. A node in that state
// looks completely healthy — it has peers, it serves its API — and is simply
// not discoverable.
type advertisementHealth struct {
	mu sync.Mutex

	attempts     int
	successes    int
	failures     int
	consecFails  int
	lastErr      string
	lastOK       time.Time
	lastAttempt  time.Time
	wasFailing   bool
	recoveredNow bool
	firstSuccess bool
	startedFail  bool
	priorFails   int
}

func (h *advertisementHealth) record(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.attempts++
	h.lastAttempt = time.Now().UTC()
	h.recoveredNow = false
	h.firstSuccess = false
	h.startedFail = false

	if err != nil {
		h.failures++
		h.consecFails++
		h.lastErr = err.Error()
		if !h.wasFailing {
			h.startedFail = true
			h.wasFailing = true
		}
		return
	}

	if h.wasFailing {
		h.recoveredNow = true
		h.priorFails = h.consecFails
	} else if h.successes == 0 {
		h.firstSuccess = true
	}
	h.successes++
	h.consecFails = 0
	h.wasFailing = false
	h.lastErr = ""
	h.lastOK = time.Now().UTC()
}

func (h *advertisementHealth) justStartedFailing() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.startedFail
}

func (h *advertisementHealth) justRecovered() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.recoveredNow
}

func (h *advertisementHealth) isFirstSuccess() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.firstSuccess
}

func (h *advertisementHealth) priorFailures() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.priorFails
}

// AdvertisementStatus is the reportable view of whether this node is findable.
type AdvertisementStatus struct {
	// Findable is true once the rendezvous announce has succeeded and is not
	// currently failing. It is the single answer an operator wants.
	Findable            bool      `json:"findable"`
	Namespace           string    `json:"namespace"`
	Flag                string    `json:"flag"`
	Attempts            int       `json:"attempts"`
	Successes           int       `json:"successes"`
	Failures            int       `json:"failures"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
	LastError           string    `json:"last_error,omitempty"`
	LastSuccess         time.Time `json:"last_success,omitempty"`
	LastAttempt         time.Time `json:"last_attempt,omitempty"`
}

func (h *advertisementHealth) snapshot() AdvertisementStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	return AdvertisementStatus{
		Findable:            h.successes > 0 && h.consecFails == 0,
		Attempts:            h.attempts,
		Successes:           h.successes,
		Failures:            h.failures,
		ConsecutiveFailures: h.consecFails,
		LastError:           h.lastErr,
		LastSuccess:         h.lastOK,
		LastAttempt:         h.lastAttempt,
	}
}

// SDNAdvertisement reports whether this node is findable through the SDN
// rendezvous on the public DHT.
func (n *Node) SDNAdvertisement() AdvertisementStatus {
	if n == nil {
		return AdvertisementStatus{}
	}
	status := n.sdnAdvertisementHealth.snapshot()
	status.Namespace = n.sdnAdvertisementTarget.Namespace
	status.Flag = n.sdnAdvertisementTarget.Flag
	return status
}

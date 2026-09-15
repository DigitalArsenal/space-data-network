package modulert

import (
	"sync"

	"github.com/spacedatanetwork/sdn-server/internal/ops"
)

// The capability gate is the one place in this package an operator needs told
// about: a module or flow bundle that asks for a sensitive capability with no
// recorded approval does not load, and before this it said so only in the
// error returned to whoever tried to mount it — which, for a flow mounted at
// boot, is a line in the log and then silence.
//
// The sink is a package variable rather than a field because
// checkCapabilityPolicy is reached from two unrelated call paths (the module
// load path and the flow-bundle provision path), neither of which carries any
// node-scoped context, and threading a registry through both would be a much
// larger change than the fact is worth. The node sets it once at start.
var (
	alertsMu sync.RWMutex
	alerts   ops.Sink
)

// SetAlerts hands this package the node's alert registry. Called once at node
// start; a nil sink disables alerting without changing any gate behavior.
func SetAlerts(sink ops.Sink) {
	alertsMu.Lock()
	defer alertsMu.Unlock()
	alerts = sink
}

func alertSink() ops.Sink {
	alertsMu.RLock()
	defer alertsMu.RUnlock()
	return alerts
}

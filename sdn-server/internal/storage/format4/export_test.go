package format4

import "github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"

// FenceForTest fences the engine's instance as a trap does.
func FenceForTest(e *Engine, cause error) bool {
	inst, ok := e.ctl.(*flatsqlrt.P4Instance)
	if !ok {
		return false
	}
	inst.Fence(cause)
	return true
}

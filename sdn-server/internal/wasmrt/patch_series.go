//go:build !wasmedge_unpatched

package wasmrt

// The runtime patch series this package requires. Patch 10-series-marker
// (scripts/build-static-wasmedge.sh) exports WasmEdge_SDNPatchSeries10 from
// libwasmedge, and this package calls it, so a binary linked against a
// runtime without patches 01 to 10 fails at link time with
//
//	undefined reference to `WasmEdge_SDNPatchSeries10'
//
// (`_WasmEdge_SDNPatchSeries10` on darwin) instead of starting with a runtime
// the self-test passes: it has no check for 08 or 09.
// Rebuild the prefix with scripts/build-static-wasmedge.sh. A build that must
// link an upstream runtime (CodeQL's Go build) says so with the
// wasmedge_unpatched build tag.

/*
#include <stdint.h>
uint32_t WasmEdge_SDNPatchSeries10(void);
static uint32_t sdn_patch_series(void) { return WasmEdge_SDNPatchSeries10(); }
*/
import "C"

// PatchSeries is the SDN runtime patch series the linked libwasmedge carries.
func PatchSeries() int { return int(C.sdn_patch_series()) }

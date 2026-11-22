//go:build wasmedge_unpatched

package wasmrt

// PatchSeries is 0 in a build made with the wasmedge_unpatched tag, which
// links whatever libwasmedge it is given and requires no patch series.
func PatchSeries() int { return 0 }

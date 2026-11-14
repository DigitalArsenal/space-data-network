package format4

import "testing"

func TestCatalogRelationBound(t *testing.T) {
	for typ, want := range map[string]uint32{"OCM": 100_000, "CAT": 100_000, "OMM": 400_000, "TBS": 400_000, "MPE": 10_000, "WXF": 10_000} {
		spec := typeStorageOptions(typ)
		if spec.A18Bound != want {
			t.Errorf("%s bound = %d, want %d", typ, spec.A18Bound, want)
		}
	}
}

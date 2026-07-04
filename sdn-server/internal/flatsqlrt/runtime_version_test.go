package flatsqlrt

import "testing"

func TestRuntimeVersionAtLeast(t *testing.T) {
	cases := []struct {
		v    string
		want bool
	}{
		{"0.14.0", false},
		{"0.15.1", false},
		{"0.16.3", false},
		{"0.16.4", true},
		{"0.16.5", true},
		{"0.17.0", true},
		{"0.17.1-rc.2", true},
		{"1.0.0", true},
		{"0.16.4-rc.1", true}, // leading numerics win; rc of the fixed patch treated fixed
		{"garbage", false},
		{"0.16", false},
		{"", false},
	}
	for _, c := range cases {
		if got := runtimeVersionAtLeast(c.v, 0, 16, 4); got != c.want {
			t.Errorf("runtimeVersionAtLeast(%q, 0.16.4) = %v, want %v", c.v, got, c.want)
		}
	}
}

func TestRuntimeVersionReported(t *testing.T) {
	v := RuntimeVersion()
	if v == "" {
		t.Fatal("RuntimeVersion() returned empty")
	}
	t.Logf("libwasmedge=%s RuntimeHasLinkedAOTFix=%v", v, RuntimeHasLinkedAOTFix())
}

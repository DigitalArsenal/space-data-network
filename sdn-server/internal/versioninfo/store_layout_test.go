package versioninfo

import (
	"bytes"
	"testing"
)

func TestStoreLayoutCompatibilityStamp(t *testing.T) {
	for _, layout := range []int{0, 1, 2} {
		data := append(make([]byte, (1<<20)-7), StoreFormatStamp(4)...)
		if layout > 0 {
			data = append(data, StoreLayoutStamp(layout)...)
		}
		format, found, actual, present, err := ReadStoreCompatibilityStamp(bytes.NewReader(data))
		if err != nil || !found || format != 4 || actual != max(1, layout) || present != (layout > 0) {
			t.Fatalf("layout %d: %d %v %d %v %v", layout, format, found, actual, present, err)
		}
	}
}

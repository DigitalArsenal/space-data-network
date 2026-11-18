package versioninfo

import (
	"bytes"
	"testing"
)

func TestStoreLayoutCompatibilityStamp(t *testing.T) {
	for _, layout := range []int{0, 1, 2} {
		data := append(make([]byte, (1<<20)-7), StoreFormatStamp(MaxStoreFormat)...)
		if layout > 0 {
			data = append(data, StoreLayoutStamp(layout)...)
		}
		format, found, actual, present, err := ReadStoreCompatibilityStamp(bytes.NewReader(data))
		if err != nil || !found || format != MaxStoreFormat || actual != max(1, layout) || present != (layout > 0) {
			t.Fatalf("layout %d: %d %v %d %v %v", layout, format, found, actual, present, err)
		}
	}
}

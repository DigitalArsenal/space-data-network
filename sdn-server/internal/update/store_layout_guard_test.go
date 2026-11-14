package update

import "testing"

func TestStoreLayoutGuard(t *testing.T) {
	store := StoreFormat{Format: 4, Layout: 2}
	for _, layout := range []int{0, 1, 2} {
		err := guardStoreFormat("rollback", "test", "", store, func() (slotStamp, bool, error) { return slotStamp{Format: 4, Layout: layout}, true, nil })
		if (err == nil) != (layout == 2) {
			t.Fatalf("layout %d: %v", layout, err)
		}
	}
}

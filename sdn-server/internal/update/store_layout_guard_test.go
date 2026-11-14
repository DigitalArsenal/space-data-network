package update

import (
	"errors"
	"testing"
)

func TestStoreLayoutGuard(t *testing.T) {
	store := StoreFormat{Format: 4, Layout: 2}
	for _, layout := range []int{0, 1, 2} {
		err := guardStoreFormat("rollback", "test", "", store, func() (slotStamp, bool, error) { return slotStamp{Format: 4, Layout: layout}, true, nil })
		if (err == nil) != (layout == 2) {
			t.Fatalf("layout %d: %v", layout, err)
		}
		if err != nil {
			var refusal *StoreFormatRefusal
			if !errors.As(err, &refusal) {
				t.Fatalf("layout refusal must be typed: %v", err)
			}
		}
	}
}

func TestLayoutOneRollbackToReleasedStamp(t *testing.T) {
	store, err := ReadStoreFormat(writeFormat4Store(t))
	if err != nil || store.Format != 4 || store.Layout != 1 {
		t.Fatalf("released store: %+v %v", store, err)
	}
	if err := guardStoreFormat("rollback", "previous release", "", store, func() (slotStamp, bool, error) {
		return slotStamp{Format: 4}, true, nil
	}); err != nil {
		t.Fatal(err)
	}
}

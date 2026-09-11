package license

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
)

func TestPluginRegistryAllowedXpubMutationIsPersistentAndIdempotent(t *testing.T) {
	root := writePluginRoot(t, []PluginCatalogEntry{{
		ID:      "com.example.protected",
		Version: "1.0.0",
	}}, "")
	reg, err := LoadPluginRegistry(root)
	if err != nil {
		t.Fatal(err)
	}

	changed, err := reg.GrantAllowedXpub("com.example.protected", "  xpub-buyer-a  ")
	if err != nil || !changed {
		t.Fatalf("first GrantAllowedXpub() = (%v, %v), want (true, nil)", changed, err)
	}
	changed, err = reg.GrantAllowedXpub("com.example.protected", "xpub-buyer-a")
	if err != nil || changed {
		t.Fatalf("duplicate GrantAllowedXpub() = (%v, %v), want (false, nil)", changed, err)
	}

	reloaded, err := LoadPluginRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	asset, ok := reloaded.Get("com.example.protected")
	if !ok || !reflect.DeepEqual(asset.AllowedXpubs, []string{"xpub-buyer-a"}) {
		t.Fatalf("persisted allowed xpubs = %#v, want buyer A", asset)
	}

	changed, err = reloaded.RevokeAllowedXpub("com.example.protected", "xpub-buyer-a")
	if err != nil || !changed {
		t.Fatalf("first RevokeAllowedXpub() = (%v, %v), want (true, nil)", changed, err)
	}
	changed, err = reloaded.RevokeAllowedXpub("com.example.protected", "xpub-buyer-a")
	if err != nil || changed {
		t.Fatalf("duplicate RevokeAllowedXpub() = (%v, %v), want (false, nil)", changed, err)
	}

	reloaded, err = LoadPluginRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	asset, ok = reloaded.Get("com.example.protected")
	if !ok || len(asset.AllowedXpubs) != 0 {
		t.Fatalf("revoked allowed xpubs = %#v, want empty", asset)
	}
}

func TestPluginRegistryAllowedXpubMutationIsConcurrentSafe(t *testing.T) {
	root := writePluginRoot(t, []PluginCatalogEntry{{
		ID:      "com.example.concurrent",
		Version: "1.0.0",
	}}, "")
	reg, err := LoadPluginRegistry(root)
	if err != nil {
		t.Fatal(err)
	}

	const buyers = 24
	var wg sync.WaitGroup
	errCh := make(chan error, buyers*2)
	for i := 0; i < buyers; i++ {
		buyer := fmt.Sprintf("xpub-buyer-%02d", i)
		for attempt := 0; attempt < 2; attempt++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := reg.GrantAllowedXpub("com.example.concurrent", buyer); err != nil {
					errCh <- err
				}
			}()
		}
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent GrantAllowedXpub() failed: %v", err)
	}

	asset, ok := reg.Get("com.example.concurrent")
	if !ok || len(asset.AllowedXpubs) != buyers {
		t.Fatalf("in-memory allowed xpub count = %d, want %d", len(asset.AllowedXpubs), buyers)
	}
	reloaded, err := LoadPluginRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	asset, ok = reloaded.Get("com.example.concurrent")
	if !ok || len(asset.AllowedXpubs) != buyers {
		t.Fatalf("persisted allowed xpub count = %d, want %d", len(asset.AllowedXpubs), buyers)
	}
}

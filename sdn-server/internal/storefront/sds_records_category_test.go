package storefront

import (
	"testing"

	sdsstf "github.com/DigitalArsenal/spacedatastandards.org/lib/go/STF"
	"github.com/spacedatanetwork/sdn-server/internal/cct"
)

// resolver stands in for the node's module-catalog join.
func resolver(byModule map[string]string) func(string) string {
	return func(moduleID string) string {
		if class, ok := byModule[moduleID]; ok {
			return class
		}
		return cct.Unspecified
	}
}

// TestListingRecordCarriesCapabilityClass closes the gap this lane was opened
// for: before SDS v1.186.0 an $STF carried no capability category at all, so a
// storefront shelf and a library shelf were grouped by two unrelated systems.
//
// The listing's category is JOINED from the module behind it, never derived
// from DATA_TYPES, TAGS or TITLE — $STF forbids that explicitly, because none
// of them is a controlled vocabulary. A data-stream listing therefore has no
// capability category, and says so.
func TestListingRecordCarriesCapabilityClass(t *testing.T) {
	t.Parallel()

	join := resolver(map[string]string{
		"com.example.sgp4":  "PROPAGATION",
		"com.example.store": "COMMERCE_AND_LICENSING",
	})

	for _, tc := range []struct {
		name    string
		listing Listing
		want    string
	}{
		{
			name: "a module listing shelves as its module's class",
			listing: Listing{
				ListingID:         "l-1",
				ListingKind:       ListingKindWASMModule,
				ProtectedDelivery: ProtectedDelivery{ModuleID: "com.example.sgp4"},
			},
			want: "PROPAGATION",
		},
		{
			name: "a second module, a different shelf",
			listing: Listing{
				ListingID:         "l-2",
				ListingKind:       ListingKindWASMModule,
				ProtectedDelivery: ProtectedDelivery{ModuleID: "com.example.store"},
			},
			want: "COMMERCE_AND_LICENSING",
		},
		{
			name: "a module the catalog never categorized",
			listing: Listing{
				ListingID:         "l-3",
				ListingKind:       ListingKindWASMModule,
				ProtectedDelivery: ProtectedDelivery{ModuleID: "com.example.unknown"},
			},
			want: cct.Unspecified,
		},
		{
			// The important negative. DATA_TYPES and TAGS below are exactly the
			// bait $STF forbids re-deriving a category from.
			name: "a data-stream listing has no capability category, and does not invent one",
			listing: Listing{
				ListingID:   "l-4",
				ListingKind: ListingKindDataStream,
				DataTypes:   []string{"OMM", "CDM"},
				Tags:        []string{"propagation", "conjunction"},
				Title:       "Propagation feed",
			},
			want: cct.Unspecified,
		},
		{
			name: "a module listing with no module ID",
			listing: Listing{
				ListingID:   "l-5",
				ListingKind: ListingKindWASMModule,
			},
			want: cct.Unspecified,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			record := sdsstf.GetRootAsSTF(encodeListingRecord(&tc.listing, join), 0)
			if got := record.PRIMARY_CATEGORY().String(); got != tc.want {
				t.Errorf("PRIMARY_CATEGORY = %q, want %q", got, tc.want)
			}

			// CATEGORIES is empty by design — a category joined from one
			// single-valued module family is always exactly one, and $STF reads
			// an empty list with a set PRIMARY_CATEGORY as membership in that
			// one category. The invariant is still asserted rather than
			// trusted, so populating it later cannot silently omit the primary.
			if n := record.CATEGORIESLength(); n > 0 {
				found := false
				for i := 0; i < n; i++ {
					if record.CATEGORIES(i) == record.PRIMARY_CATEGORY() {
						found = true
						break
					}
				}
				if !found {
					t.Error("CATEGORIES is nonempty but omits PRIMARY_CATEGORY")
				}
			}
		})
	}
}

// TestListingRecordWithoutResolverIsUnspecified: a store nobody wired a catalog
// into knows no categories. That must read as UNSPECIFIED — never as the
// vocabulary's first member, which is the failure $CCT's ordinal-0 choice
// exists to prevent.
func TestListingRecordWithoutResolverIsUnspecified(t *testing.T) {
	t.Parallel()

	listing := Listing{
		ListingID:         "l-1",
		ListingKind:       ListingKindWASMModule,
		ProtectedDelivery: ProtectedDelivery{ModuleID: "com.example.sgp4"},
	}
	record := sdsstf.GetRootAsSTF(encodeListingRecord(&listing, nil), 0)
	if got := record.PRIMARY_CATEGORY().String(); got != cct.Unspecified {
		t.Errorf("PRIMARY_CATEGORY = %q, want %q", got, cct.Unspecified)
	}
	if got := record.PRIMARY_CATEGORY(); got != 0 {
		t.Errorf("PRIMARY_CATEGORY ordinal = %d, want 0", got)
	}
}

// TestListingCategoryIsNotSelfDeclared: the shelf is derived from the catalog
// join, so a client cannot POST its own. A listing that arrives already
// carrying PRIMARY_CATEGORY must be encoded with the joined value, not the
// claimed one.
func TestListingCategoryIsNotSelfDeclared(t *testing.T) {
	t.Parallel()

	listing := Listing{
		ListingID:         "l-1",
		ListingKind:       ListingKindWASMModule,
		ProtectedDelivery: ProtectedDelivery{ModuleID: "com.example.sgp4"},
		PrimaryCategory:   "CONJUNCTION_ASSESSMENT", // the claim
	}
	join := resolver(map[string]string{"com.example.sgp4": "PROPAGATION"})

	record := sdsstf.GetRootAsSTF(encodeListingRecord(&listing, join), 0)
	if got := record.PRIMARY_CATEGORY().String(); got != "PROPAGATION" {
		t.Errorf("PRIMARY_CATEGORY = %q, want PROPAGATION — a self-declared category was honoured", got)
	}
}

// TestDataTypeIndexKeepsSDSCapitalization guards the defect Themis named while
// ratifying $CCT: the DHT index called "category" was a DATA-TYPE index, and it
// lowercased the codes into its keys. SDS type codes are identifiers, not
// prose — "$OMM" is not "omm" — so the folded key could never be hit by a
// canonical lookup.
func TestDataTypeIndexKeepsSDSCapitalization(t *testing.T) {
	t.Parallel()

	catalog := &Catalog{entries: map[string]*CatalogEntry{
		"l-1": {ListingID: "l-1", Active: true, DataTypes: []string{"OMM", "CDM"}},
		"l-2": {ListingID: "l-2", Active: true, DataTypes: []string{"CDM"}},
	}}

	if got := catalog.getDataTypeListingIDsLocked("OMM"); len(got) != 1 || got[0] != "l-1" {
		t.Errorf("lookup by the canonical code returned %v, want [l-1]", got)
	}
	if got := catalog.getDataTypeListingIDsLocked("CDM"); len(got) != 2 {
		t.Errorf("lookup by CDM returned %v, want 2 listings", got)
	}
	if got := catalog.getDataTypeListingIDsLocked("omm"); len(got) != 0 {
		t.Errorf("a lowercased code matched %v; the index is keyed by the SDS code verbatim", got)
	}
}

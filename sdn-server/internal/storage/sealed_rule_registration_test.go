package storage

// A standard whose sealed field an extraction rule reads (format2.
// ErrSealedRuleField) is left unregistered on a bounded and an unlimited
// stream store alike: the node starts, every write to it is refused with that
// error, and a statement naming it is an error. Any other refusal of an
// unlimited registration still aborts before the store is promoted.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/encfield"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format2"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/marker"
)

func sealPNMFileID(t *testing.T) {
	t.Helper()
	encfield.RegisterSchema("PNM", []encfield.FieldSpec{{Name: "FILE_ID", FieldID: 4}})
	t.Cleanup(func() { encfield.RegisterSchema("PNM", nil) })
}

func TestSealedRuleRefusalsNameTheStandard(t *testing.T) {
	v, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := SealedRuleRefusals(v); len(got) != 0 {
		t.Fatalf("embedded standards refused: %+v", got)
	}
	sealPNMFileID(t)
	got := SealedRuleRefusals(v)
	if len(got) != 1 || got[0].Schema != "PNM.fbs" || !errors.Is(got[0].Err, format2.ErrSealedRuleField) {
		t.Fatalf("sealed PNM.FILE_ID: %+v", got)
	}
}

// The same registration on a bounded and an unlimited store: the same start,
// the same refusal of the standard's writes, the same answer to a read.
func TestSealedRuleStandardIsTheSameBoundedAndUnlimited(t *testing.T) {
	onFormat4Engine(t, func(t *testing.T) {
		sealPNMFileID(t)
		recs := [][]byte{
			buildPNMForTest(0, fmt.Sprintf("ZQSEALFID0x%x", time.Now().UnixNano()), "sig0"),
			buildPNMForTest(1, fmt.Sprintf("ZQSEALFID1x%x", time.Now().UnixNano()), "sig1"),
			buildPNMForTest(2, fmt.Sprintf("ZQSEALFID2x%x", time.Now().UnixNano()), "sig2"),
		}
		type answer struct {
			writes  [3]bool // each write refused with ErrSealedRuleField
			raw     bool    // QueryRawStream naming PNM: ErrNoSQLRelation
			count   int64
			countOK bool
		}
		var answers []answer
		for _, unlimited := range []bool{false, true} {
			root := t.TempDir()
			s := openFormat4ForTest(t, root, WithUnlimitedSQLVisibility(unlimited))
			var a answer
			_, e1 := s.Store("PNM.fbs", recs[0], "source:pnm", nil)
			_, e2 := s.StoreBatch("PNM.fbs", recs[1:2], "source:pnm", nil)
			_, e3 := s.StoreWithSourceTags("PNM.fbs", recs[2], "source:pnm", nil, SourceTags{ProviderID: "proof", SourceName: "sealed-pnm"})
			for i, err := range []error{e1, e2, e3} {
				a.writes[i] = errors.Is(err, format2.ErrSealedRuleField)
			}
			st, err := s.QueryRawStream("SELECT _data FROM PNM")
			a.raw = errors.Is(err, ErrNoSQLRelation) && st == nil
			n, err := s.Count("PNM.fbs")
			a.count, a.countOK = n, err == nil
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			m, err := marker.Read(root)
			want := uint16(1)
			if unlimited {
				want = 2
			}
			if err != nil || m.Layout != want {
				t.Fatalf("unlimited=%v: the store opened at %+v (%v), want layout %d", unlimited, m, err, want)
			}
			if a.writes != [3]bool{true, true, true} || !a.raw {
				t.Fatalf("unlimited=%v: writes refused %v, raw stream ErrNoSQLRelation %v", unlimited, a.writes, a.raw)
			}
			answers = append(answers, a)
		}
		if answers[0] != answers[1] {
			t.Fatalf("bounded %+v, unlimited %+v", answers[0], answers[1])
		}
	})
}

// refusingAPI is the engine with ValidateType refusing one type: a second,
// different refusal in the same registration.
type refusingAPI struct {
	format4.API
	refuse string
}

func (r refusingAPI) ValidateType(spec format4.TypeSpec) error {
	if spec.TypeName() == r.refuse {
		return errors.New("injected: an invalid type spec")
	}
	return r.API.ValidateType(spec)
}

// Only the sealed-rule refusal is skipped: any other refusal of an unlimited
// registration aborts before the first registration, so the bounded store it
// would have promoted stays at layout 1 with no promotion intent.
func TestUnlimitedRegistrationOtherRefusalAbortsBeforePromotion(t *testing.T) {
	onFormat4Engine(t, func(t *testing.T) {
		sealPNMFileID(t)
		root := t.TempDir()
		s := openFormat4ForTest(t, root, WithUnlimitedSQLVisibility(false))
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		prev := format4OpenEngine
		format4OpenEngine = func(ctx context.Context, opt format4.Options) (format4.API, error) {
			api, err := prev(ctx, opt)
			if err != nil {
				return nil, err
			}
			return refusingAPI{API: api, refuse: "OMM"}, nil
		}
		t.Cleanup(func() { format4OpenEngine = prev })
		t.Setenv(format4.FormatEnv, "4")
		v, err := sds.NewValidator(nil)
		if err != nil {
			t.Fatal(err)
		}
		unexpected, err := NewFlatSQLStore(root, v, WithUnlimitedSQLVisibility(true))
		if unexpected != nil {
			unexpected.Close()
		}
		if err == nil || errors.Is(err, format2.ErrSealedRuleField) ||
			!strings.Contains(err.Error(), "registration preflight OMM.fbs") || !strings.Contains(err.Error(), "injected") {
			t.Fatalf("a second refusal must abort the unlimited registration: %v", err)
		}
		m, err := marker.Read(root)
		if err != nil || m.Layout != 1 || m.RequiredLayout != 1 {
			t.Fatalf("aborted registration changed the store: %+v %v", m, err)
		}
		if _, err := os.Stat(filepath.Join(root, marker.Dir, "STORE.unlimited")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("aborted registration left a promotion intent: %v", err)
		}
	})
}

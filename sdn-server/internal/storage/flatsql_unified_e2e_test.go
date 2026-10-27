package storage

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/CAT"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// The independent oracle is flatc's generated SDS accessors, not the engine's
// reflection code or the retired engine's signed-uint32/null-default behavior.
// Nested fields remain in the original binary and are checked byte for byte.
func TestUnifiedStorageCanonicalFieldsReopen(t *testing.T) {
	onFormat4Engine(t, func(t *testing.T) {
		root := t.TempDir()
		store := openFormat4ForTest(t, root)
		defer func() { _ = store.Close() }()
		b := flatbuffers.NewBuilder(256)
		name, id := b.CreateString("München field proof"), b.CreateString("2026-999A")
		CAT.CATStart(b)
		CAT.CATAddOBJECT_NAME(b, name)
		CAT.CATAddOBJECT_ID(b, id)
		CAT.CATAddNORAD_CAT_ID(b, 4000000001)
		CAT.CATAddINCLINATION(b, 51.625)
		CAT.CATAddMANEUVERABLE(b, true)
		CAT.FinishCATBuffer(b, CAT.CATEnd(b))
		cat := append([]byte(nil), b.FinishedBytes()...)
		omm := sds.NewOMMBuilder().WithNoradCatID(4000000001).WithObjectName("Orbit field proof").WithObjectID("2026-999A").Build()[4:]
		fixtures := []struct {
			name   string
			data   []byte
			oracle any
		}{
			{"CAT", cat, CAT.GetRootAsCAT(cat, 0)},
			{"OMM", omm, OMM.GetRootAsOMM(omm, 0)},
		}
		signature := []byte{1, 3, 5, 7}
		tags := SourceTags{ProviderID: "proof", SourceName: "canonical", BatchID: "batch-1"}
		for _, f := range fixtures {
			if _, err := store.StoreWithSourceTags(f.name+".fbs", f.data, "proof-peer", signature, tags); err != nil {
				t.Fatal(err)
			}
		}
		fields := regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
		for pass := 0; pass < 2; pass++ {
			for _, f := range fixtures {
				got, err := store.GetRecord(f.name+".fbs", ComputeCID(f.data))
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got.Data, f.data) || !bytes.Equal(got.Signature, signature) || got.PeerID != "proof-peer" {
					t.Fatalf("%s reopen=%d record or provenance differs: %+v", f.name, pass, got)
				}
				recent, err := store.QueryRecentRecords(f.name+".fbs", 10)
				if err != nil || len(recent) != 1 || recent[0].SourceTags.ProviderID != tags.ProviderID || recent[0].SourceTags.SourceName != tags.SourceName || recent[0].SourceTags.BatchID != tags.BatchID {
					t.Fatalf("%s reopen=%d source tags differ: %+v %v", f.name, pass, recent, err)
				}
				receiver := reflect.ValueOf(f.oracle)
				var columns, expected []string
				for i := 0; i < receiver.Type().NumMethod(); i++ {
					method := receiver.Type().Method(i)
					if !fields.MatchString(method.Name) || method.Type.NumIn() != 1 || method.Type.NumOut() != 1 {
						continue
					}
					value := receiver.Method(i).Call(nil)[0]
					var text string
					switch value.Kind() {
					case reflect.Bool:
						if value.Bool() {
							text = "1"
						} else {
							text = "0"
						}
					case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
						text = strconv.FormatInt(value.Int(), 10)
					case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
						text = strconv.FormatUint(value.Uint(), 10)
					case reflect.Float32, reflect.Float64:
						text = strconv.FormatFloat(value.Float(), 'g', -1, 64)
					case reflect.Slice:
						if value.Type().Elem().Kind() != reflect.Uint8 {
							continue
						}
						text = string(value.Bytes())
					default:
						continue
					}
					columns = append(columns, method.Name)
					expected = append(expected, text)
				}
				result, err := store.SandboxedSelect(context.Background(), "SELECT "+strings.Join(columns, ",")+" FROM "+f.name+" WHERE NORAD_CAT_ID > 2147483647", SandboxSelectCaps{MaxRows: 10, MaxBytes: 1 << 20, Timeout: time.Minute})
				if err != nil {
					t.Fatal(err)
				}
				if len(result.Rows) != 1 || !reflect.DeepEqual(result.Columns, columns) || !reflect.DeepEqual(result.Rows[0], expected) {
					t.Fatalf("%s reopen=%d field mismatch\ncolumns=%v\nactual=%v\nexpected=%v", f.name, pass, columns, result.Rows, expected)
				}
				t.Logf("%s reopen=%d: %d scalar/string fields match generated SDS accessors; complete binary and provenance identical", f.name, pass, len(columns))
			}
			if pass == 0 {
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				store = openFormat4ForTest(t, root)
			}
		}
		for _, sql := range []string{"SELECT json('{}')", "SELECT geo_from_geojson('{}')"} {
			if _, err := store.SandboxedSelect(context.Background(), sql, SandboxSelectCaps{MaxRows: 10, MaxBytes: 1 << 20, Timeout: time.Minute}); err == nil {
				t.Fatal(fmt.Sprintf("removed conversion accepted: %s", sql))
			}
		}
	})
}

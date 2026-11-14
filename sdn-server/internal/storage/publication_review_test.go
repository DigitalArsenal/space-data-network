package storage

import (
	"context"
	"errors"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
	"os"
	"path/filepath"
	"testing"
)

type publicationRejectAPI struct{ format4.API }

func (a publicationRejectAPI) Put(ctx context.Context, b format4.Batch) ([]format4.Outcome, error) {
	if len(b.Records) != 2 {
		return a.API.Put(ctx, b)
	}
	b.Records = b.Records[:1]
	out, err := a.API.Put(ctx, b)
	return append(out, format4.Outcome{Action: format4.ActRejected, Reject: format4.RejectTooLarge}), err
}
func TestPublicationReviewRefusalAndMarkerRecovery(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	dir := t.TempDir()
	s := journalTestStore(t, dir)
	if err := s.ConfigurePublicationSources([]PublicationSource{{Schema: "CAT.fbs", Provider: "synthetic", Source: "index"}, {Schema: "CAT.fbs", Provider: "synthetic", Source: "other"}}); err != nil {
		t.Fatal(err)
	}
	b := s.rb.(format4Backend)
	api := b.d.engine.Load()
	b.d.engine.Store(&format4Engine{publicationRejectAPI{api.API}})
	rows := [][]byte{sds.NewCATBuilder().WithNoradCatID(900001).Build(), sds.NewCATBuilder().WithNoradCatID(900002).Build()}
	n, err := b.storeBatch("CAT.fbs", rows, "synthetic", nil, &SourceTags{ProviderID: "synthetic", SourceName: "index", BatchID: "mixed"})
	var refusal *RefusedRecordsError
	if n != 1 || !errors.As(err, &refusal) {
		t.Fatalf("accepted=%d error=%v", n, err)
	}
	p, err := s.SelectPublication("CAT.fbs", "synthetic", "index", "mixed")
	if err != nil {
		t.Fatal(err)
	}
	if _, n := p.Fingerprint(); n != 1 {
		t.Fatalf("export includes refused record: %d", n)
	}
	b.d.engine.Store(api)
	s.publicationFault = func(stage string) error { return errors.New("synthetic acknowledgement I/O") }
	_, err = b.storeBatch("CAT.fbs", rows[:1], "synthetic", nil, &SourceTags{ProviderID: "synthetic", SourceName: "index", BatchID: "marker"})
	if err == nil {
		t.Fatal("marker failure hidden")
	}
	if _, err = b.storeBatch("CAT.fbs", rows[:1], "synthetic", nil, &SourceTags{ProviderID: "synthetic", SourceName: "other", BatchID: "unaffected"}); err == nil {
		t.Fatal("fault hook must be removed for other write")
	}
	s.publicationFault = nil
	s.RetryPublicationRecovery()
	if len(s.PublicationRecoveryStatus()) != 0 {
		t.Fatal("completion retry failed")
	}
	for _, batch := range []string{"mixed", "marker"} {
		if _, err = s.SelectPublication("CAT.fbs", "synthetic", "index", batch); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = journalTestStore(t, dir)
	defer s.Close()
	p, err = s.SelectPublication("CAT.fbs", "synthetic", "index", "mixed")
	if err != nil {
		t.Fatal(err)
	}
	if _, n := p.Fingerprint(); n != 1 {
		t.Fatal("refusal lost on restart")
	}
}
func TestPublicationReviewCorruptionIsolation(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	dir := t.TempDir()
	s := journalTestStore(t, dir)
	if err := s.ConfigurePublicationSources([]PublicationSource{{Schema: "CAT.fbs", Provider: "synthetic", Source: "index"}, {Schema: "CAT.fbs", Provider: "synthetic", Source: "other"}}); err != nil {
		t.Fatal(err)
	}
	journalTestPut(t, s, "initial")
	path := filepath.Join(s.publicationEntryDir(1), "records.sds")
	s.Close()
	if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	s = journalTestStore(t, dir)
	defer s.Close()
	if len(s.PublicationRecoveryStatus()) != 1 {
		t.Fatal("corrupt source not isolated")
	}
	raw := sds.NewCATBuilder().WithNoradCatID(900003).Build()
	if _, err := s.StoreWithSourceTags("CAT.fbs", raw, "synthetic", nil, SourceTags{ProviderID: "synthetic", SourceName: "other", BatchID: "good"}); err != nil {
		t.Fatal("other retained source blocked", err)
	}
}

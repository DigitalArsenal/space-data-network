package api

import (
	"net/http/httptest"
	"testing"
	"time"

	PNM "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"

	"github.com/spacedatanetwork/sdn-server/internal/pinpolicy"
)

// The pin policy answer carries the documents the node pins whatever the
// rules say, one $PNM each after the $PIN: name, CID, /ipfs/ path and when
// the pin was last confirmed. A document not (yet) pinned is left out.
func TestPinPolicyAnswerCarriesKeptDocuments(t *testing.T) {
	const cid = "bafkreih7wfkdtdkfpj6mzvv5il3tguckcivtnqu7gqi6dvosq6xe56swre"
	checked := time.Date(2026, 11, 14, 21, 7, 43, 0, time.UTC)
	docs := keptDocumentFrames([]pinpolicy.KeptDocument{
		{Name: "README.md", CID: cid, Size: 5659, Pinned: true, CheckedAt: checked},
		{Name: "pending.md", CID: "", Pinned: false},
		{Name: "unpinned.md", CID: cid, Pinned: false},
	})
	if len(docs) != 1 {
		t.Fatalf("frames = %d, want 1", len(docs))
	}
	policy, err := pinpolicy.New("", nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	writePinPolicy(rec, policy.Encode(), docs...)
	frames, err := ReadFrames(rec.Body, 1<<20)
	if err != nil || len(frames) != 2 {
		t.Fatalf("stream: %d frames, %v", len(frames), err)
	}
	if FrameIdentifier(frames[0]) != pinIdentifier || FrameIdentifier(frames[1]) != "$PNM" {
		t.Fatalf("identifiers %q %q", FrameIdentifier(frames[0]), FrameIdentifier(frames[1]))
	}
	pnm := PNM.GetSizePrefixedRootAsPNM(frames[1], 0)
	if got := string(pnm.FILE_NAME()); got != "README.md" {
		t.Errorf("FILE_NAME = %q", got)
	}
	if got := string(pnm.CID()); got != cid {
		t.Errorf("CID = %q", got)
	}
	if got := string(pnm.MULTIFORMAT_ADDRESS()); got != "/ipfs/"+cid {
		t.Errorf("MULTIFORMAT_ADDRESS = %q", got)
	}
	if got := string(pnm.PUBLISH_TIMESTAMP()); got != "2026-11-14T21:07:43Z" {
		t.Errorf("PUBLISH_TIMESTAMP = %q", got)
	}
}

package pinpolicy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeKubo answers add and pin/ls the way Kubo does for one file: the CID is
// derived from the bytes, add with pin=true pins it, and a test can unpin.
type fakeKubo struct {
	mu     sync.Mutex
	pinned map[string]bool
	adds   int
	query  map[string]string
}

func (f *fakeKubo) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/api/v0/add":
			reader, err := r.MultipartReader()
			if err != nil {
				t.Errorf("add: %v", err)
				return
			}
			part, _ := reader.NextPart()
			body, _ := io.ReadAll(part)
			cid := "bafkrei" + strings.Repeat("a", 52-len(body)%7)
			f.adds++
			f.query = map[string]string{}
			for key := range r.URL.Query() {
				f.query[key] = r.URL.Query().Get(key)
			}
			if r.URL.Query().Get("pin") == "true" {
				f.pinned[cid] = true
			}
			_, _ = io.WriteString(w, `{"Name":"README.md","Hash":"`+cid+`","Size":"`+itoa(len(body))+`"}`+"\n")
		case "/api/v0/pin/ls":
			if f.pinned[r.URL.Query().Get("arg")] {
				_, _ = io.WriteString(w, `{"Keys":{}}`)
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"Message":"path is not pinned","Code":0,"Type":"error"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for ; n > 0; n /= 10 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
	}
	return string(digits)
}

func TestKeptDocumentIsPinnedAndPinnedAgainWhenRemoved(t *testing.T) {
	kubo := &fakeKubo{pinned: map[string]bool{}}
	server := httptest.NewServer(kubo.handler(t))
	defer server.Close()
	p, err := New(server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	doc := Document{Name: "README.md", Bytes: []byte("# Space Data Network\n")}
	p.documents.docs = []Document{doc}
	p.documents.kept = []KeptDocument{{Name: doc.Name}}

	if !p.CheckDocuments(context.Background()) {
		t.Fatal("first check did not pin the document")
	}
	kept := p.Documents()
	if len(kept) != 1 || kept[0].CID == "" || !kept[0].Pinned || kept[0].Name != "README.md" {
		t.Fatalf("kept = %+v", kept)
	}
	// The CID depends on the bytes alone: CIDv1, raw leaves, sha2-256, fixed chunker.
	for key, want := range map[string]string{"cid-version": "1", "raw-leaves": "true", "hash": "sha2-256", "chunker": "size-262144", "pin": "true"} {
		if kubo.query[key] != want {
			t.Errorf("add %s = %q, want %q", key, kubo.query[key], want)
		}
	}

	// Pinned: a check only asks, it adds nothing.
	p.CheckDocuments(context.Background())
	if kubo.adds != 1 {
		t.Fatalf("a pinned document was added again (%d adds)", kubo.adds)
	}

	// Someone removes the pin: the next check pins it again, at the same CID.
	kubo.mu.Lock()
	delete(kubo.pinned, kept[0].CID)
	kubo.mu.Unlock()
	if !p.CheckDocuments(context.Background()) {
		t.Fatal("the removed pin was not pinned again")
	}
	if again := p.Documents(); again[0].CID != kept[0].CID || !again[0].Pinned || kubo.adds != 2 || !kubo.pinned[kept[0].CID] {
		t.Fatalf("after re-pin: %+v, adds %d", again, kubo.adds)
	}
}

func TestKeptDocumentReportsUnpinnedWhileKuboIsDown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	p, _ := New(server.URL, nil)
	p.documents.docs = []Document{{Name: "README.md", Bytes: []byte("x")}}
	p.documents.kept = []KeptDocument{{Name: "README.md"}}
	if p.CheckDocuments(context.Background()) {
		t.Fatal("a check against a failing Kubo reported the document pinned")
	}
	if kept := p.Documents(); kept[0].Pinned || kept[0].CID != "" {
		t.Fatalf("kept = %+v", kept)
	}
}

func TestNoKuboKeepsNothing(t *testing.T) {
	p, _ := New("", nil)
	p.KeepDocuments(context.Background(), []Document{{Name: "README.md", Bytes: []byte("x")}})
	if len(p.Documents()) != 0 || p.CheckDocuments(context.Background()) {
		t.Fatal("a node without Kubo reports a kept document")
	}
}

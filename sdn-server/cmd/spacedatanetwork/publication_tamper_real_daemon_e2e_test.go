package main

import (
	"crypto/rand"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func TestPublicationInvalidSignatureRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for announcement rejection between real daemons")
	}
	exe := publicationBinary(t)
	a := newPublicationDaemon(t, exe, "tamper-producer", "", "", "keep-all")
	a.start(t)
	b := newPublicationDaemon(t, exe, "tamper-receiver", a.address, a.id, "keep-all")
	b.start(t)
	// This valid CID names synthetic bytes. The detached signature is random,
	// so only a signature rejection is accepted as evidence of this case.
	cid, err := storage.DatasetManifestCID([]byte("synthetic invalid-signature manifest"))
	if err != nil {
		t.Fatal(err)
	}
	signature := make([]byte, 64)
	if _, err := rand.Read(signature); err != nil {
		t.Fatal(err)
	}
	announcement, err := storage.BuildDatasetPublicationPNM(&storage.DatasetPublicationManifest{CID: cid, FileID: "$OMM", Signature: signature}, storage.DatasetPublicationPNMOptions{FileName: "synthetic-manifest.dpm"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Second)
	a.request(t, "POST", "/api/v1/pubsub/publish", publicationStreamType, announcement, http.StatusOK)
	deadline := time.Now().Add(30 * time.Second)
	for {
		text := readFileString(b.logPath)
		if strings.Contains(text, "signature verification failed") || strings.Contains(text, "invalid signature") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no signature rejection in %s", b.logPath)
		}
		time.Sleep(time.Second)
	}
	b.requireRecords(t, "OMM.fbs", "", map[string]int{})
}

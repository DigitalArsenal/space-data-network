package main

import (
	"bytes"
	"crypto/rand"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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
	announcement, err := storage.BuildDatasetPublicationPNM(&storage.DatasetPublicationManifest{CID: cid, FileID: "synthetic:OMM.fbs:first", Signature: signature}, storage.DatasetPublicationPNMOptions{FileName: "synthetic-manifest.dpm"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Second)
	a.request(t, "POST", "/api/v1/pubsub/publish", publicationStreamType, announcement, http.StatusOK)
	deadline := time.Now().Add(30 * time.Second)
	for {
		text := readFileString(b.logPath)
		rejected := false
		for _, line := range strings.Split(text, "\n") {
			if strings.Contains(line, "PNM") && strings.Contains(line, "signature") && (strings.Contains(line, "invalid") || strings.Contains(line, "failed")) {
				rejected = true
			}
		}
		if rejected {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no signature rejection in %s", b.logPath)
		}
		time.Sleep(time.Second)
	}
	b.requireRecords(t, "OMM.fbs", "", map[string]int{})
}

// Corrupt a test-owned cached manifest while the receiver is stopped. This
// exercises importer content addressing independently of transport integrity.
func TestPublicationContentAddressRealDaemons(t *testing.T) {
	if os.Getenv("SDN_E2E_PUBLICATION") != "1" {
		t.Skip("set SDN_E2E_PUBLICATION=1 for real-daemon content address rejection")
	}
	exe := publicationBinary(t)
	a := newPublicationDaemon(t, exe, "content-producer", "", "", "keep-all")
	a.start(t)
	b := newPublicationDaemon(t, exe, "content-receiver", a.address, a.id, "keep-all")
	// The receiver does not subscribe to the lane, so nothing populates it:
	// the PNM's manifest content-address check runs before the lane gate.
	b.start(t)
	b.stop(t)
	a.ingestOMM(t, "content-address", "first", 700000, 16)
	pub := a.publish(t, "OMM.fbs", "content-address", "first")
	original := a.kuboRPC(t, "block/get", url.Values{"arg": {pub.ManifestCID}})
	if len(original) == 0 {
		t.Fatal("empty manifest")
	}
	var relative string
	err := filepath.WalkDir(filepath.Join(a.store, "kubo", "blocks"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".data" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Equal(data, original) {
			relative, err = filepath.Rel(a.store, path)
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if relative == "" {
		t.Fatal("published manifest block absent from producer store")
	}
	corrupt := append([]byte(nil), original...)
	corrupt[len(corrupt)-1] ^= 1
	actual, err := storage.DatasetManifestCID(corrupt)
	if err != nil || actual == pub.ManifestCID {
		t.Fatal("fixture did not change the manifest content address")
	}
	target := filepath.Join(b.store, relative)
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	b.start(t)
	time.Sleep(3 * time.Second)
	a.publishMode(t, "OMM.fbs", "content-address", "first", true)
	deadline := time.Now().Add(90 * time.Second)
	for {
		rejected := false
		for _, line := range strings.Split(readFileString(b.logPath), "\n") {
			if strings.Contains(line, pub.ManifestCID) && strings.Contains(line, "does not match expected CID") {
				rejected = true
			}
		}
		if rejected {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no content-address rejection for manifest %s in %s", pub.ManifestCID, b.logPath)
		}
		time.Sleep(time.Second)
	}
	b.requireRecords(t, "OMM.fbs", "content-address", map[string]int{})
}

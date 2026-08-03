package caps

// Dataset-publication licence carriage (graph task
// sdn-dataset-publication-license-carriage): storage.ingest_with_source is the
// ONLY authority for a batch's licence — the parser node that fetched the
// document declares its terms, the host records them against the batch, and
// publication binds them into the DPM. These tests pin that seam.

import (
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestIngestWithSourceRecordsBatchLicense(t *testing.T) {
	handler, store := newIngestTestHandler(t, StorageCapOptions{
		RawRoot:          filepath.Join(t.TempDir(), "raw"),
		MinFreeDiskBytes: 1,
	})

	stream := sizePrefixedStream([][]byte{
		buildIngestTestOMM(t, 7101, 1700000000),
		buildIngestTestOMM(t, 7102, 1700000000),
	})
	payload := map[string]interface{}{
		"schema":      "OMM.fbs",
		"provider_id": "space-data-network-02",
		"source_name": "satnogs-rfb",
		"source_url":  "https://db.satnogs.org/api/transmitters/",
		"batch_id":    "batch-license-1",
		"license":     "CC-BY-SA-4.0",
		"license_url": "https://creativecommons.org/licenses/by-sa/4.0/",
		"citation":    "SatNOGS DB contributors, CC BY-SA 4.0",
		"share_alike": true,
		"records":     base64.StdEncoding.EncodeToString(stream),
	}
	body, _ := json.Marshal(payload)
	resp, err := handler("storage.ingest_with_source", body)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	meta := decodeCapMeta(t, resp)
	if ok, _ := meta["ok"].(bool); !ok {
		t.Fatalf("licensed ingest failed: %v", meta)
	}

	license, found, err := store.SourceBatchLicenseFor("OMM.fbs", "space-data-network-02", "satnogs-rfb", "batch-license-1")
	if err != nil {
		t.Fatalf("SourceBatchLicenseFor: %v", err)
	}
	if !found {
		t.Fatalf("ingest did not record the declared batch licence")
	}
	if license.License != "CC-BY-SA-4.0" {
		t.Fatalf("LICENSE = %q", license.License)
	}
	if license.LicenseURL != "https://creativecommons.org/licenses/by-sa/4.0/" {
		t.Fatalf("LICENSE_URL = %q", license.LicenseURL)
	}
	if license.Citation != "SatNOGS DB contributors, CC BY-SA 4.0" {
		t.Fatalf("CITATION = %q", license.Citation)
	}
	if !license.ShareAlike {
		t.Fatalf("share_alike was dropped: %+v", license)
	}
}

func TestIngestWithSourceWithoutLicenseRecordsNothing(t *testing.T) {
	handler, store := newIngestTestHandler(t, StorageCapOptions{
		RawRoot:          filepath.Join(t.TempDir(), "raw"),
		MinFreeDiskBytes: 1,
	})

	stream := sizePrefixedStream([][]byte{buildIngestTestOMM(t, 7201, 1700000000)})
	body, _ := json.Marshal(map[string]interface{}{
		"schema":      "OMM.fbs",
		"provider_id": "space-data-network-02",
		"source_name": "provider-gp",
		"batch_id":    "batch-no-license",
		"records":     base64.StdEncoding.EncodeToString(stream),
	})
	resp, err := handler("storage.ingest_with_source", body)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if ok, _ := decodeCapMeta(t, resp)["ok"].(bool); !ok {
		t.Fatalf("unlicensed ingest failed: %v", decodeCapMeta(t, resp))
	}

	if _, found, err := store.SourceBatchLicenseFor("OMM.fbs", "space-data-network-02", "provider-gp", "batch-no-license"); err != nil {
		t.Fatalf("SourceBatchLicenseFor: %v", err)
	} else if found {
		t.Fatalf("ingest without licence metadata invented a licence row")
	}
}

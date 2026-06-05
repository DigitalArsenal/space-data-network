package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"
	flatbuffers "github.com/google/flatbuffers/go"
)

func TestVerifyReleaseDirectoryAcceptsCompleteReleaseEvidence(t *testing.T) {
	root := t.TempDir()

	releasePLG := buildTestReleasePLG(t)
	writeReleaseTestFile(t, root, "release.plg", releasePLG)
	releaseHash := sha256.Sum256(releasePLG)
	writeReleaseTestFile(t, root, "release.pnm", buildTestReleasePNM(t, "bafybeireleaseplgcid", "bitcoin-proof-ref"))
	writeReleaseTestJSON(t, root, "release-bitcoin.json", map[string]any{
		"releasePublicationHash": "sha256:" + hex.EncodeToString(releaseHash[:]),
		"bitcoinSignature":       "3045022100abcdef022001",
		"bitcoinPublicKey":       "0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798",
		"bitcoinAddress":         "bc1qreleaseevidence00000000000000000000000",
		"network":                "signet",
		"anchorMethod":           "op_return",
		"transactionId":          strings.Repeat("a", 64),
		"outputIndex":            0,
		"blockHeight":            1,
		"confirmations":          6,
		"verificationInstructions": []string{
			"sha256sum -c spacedatanetwork-checksums.txt",
			"verify the OP_RETURN commitment matches releasePublicationHash",
		},
	})
	writeReleaseTestJSON(t, root, "ipfs-deployment.json", map[string]any{
		"sdnAdmin":  map[string]any{"cid": "bafybeisndadmin", "path": "/ipfs/bafybeisndadmin"},
		"ipfsWebui": map[string]any{"cid": "bafybeiwebui", "path": "/ipfs/bafybeiwebui"},
	})
	writeReleaseTestJSON(t, root, "spacedatanetwork-sbom.cdx.json", map[string]any{
		"bomFormat":   "CycloneDX",
		"specVersion": "1.5",
	})
	writeReleaseTestJSON(t, root, "container-digests.json", map[string]any{
		"images": []map[string]any{
			{"name": "dockerdigitalarsenal/space-data-network", "digest": "sha256:" + strings.Repeat("b", 64)},
		},
	})
	for _, name := range []string{
		"spacedatanetwork-full-1.2.3.rpm",
		"spacedatanetwork-edge-1.2.3.rpm",
		"spacedatanetwork-full_1.2.3_amd64.deb",
		"spacedatanetwork-edge_1.2.3_amd64.deb",
		"spacedatanetwork-linux-vm-1.2.3.tar.gz",
	} {
		writeReleaseTestFile(t, root, name, []byte("artifact:"+name))
	}
	writePortableCLITarGz(t, root, "spacedatanetwork-1.2.3-darwin-arm64.tar.gz", "spacedatanetwork-1.2.3-darwin-arm64", false)
	writePortableCLITarGz(t, root, "spacedatanetwork-1.2.3-darwin-amd64.tar.gz", "spacedatanetwork-1.2.3-darwin-amd64", false)
	writePortableCLITarGz(t, root, "spacedatanetwork-1.2.3-linux-amd64.tar.gz", "spacedatanetwork-1.2.3-linux-amd64", false)
	writePortableCLITarGz(t, root, "spacedatanetwork-1.2.3-linux-arm64.tar.gz", "spacedatanetwork-1.2.3-linux-arm64", false)
	writePortableCLIZip(t, root, "spacedatanetwork-1.2.3-windows-amd64.zip", "spacedatanetwork-1.2.3-windows-amd64", false)
	writeChecksums(t, root)

	report, err := verifyReleaseDirectory(root)
	if err != nil {
		t.Fatalf("verifyReleaseDirectory failed: %v", err)
	}
	if !report.OK {
		t.Fatalf("report = %#v, want OK", report)
	}
	if len(report.Artifacts) < 10 {
		t.Fatalf("artifacts = %#v, want release artifacts reported", report.Artifacts)
	}
}

func TestVerifyPortableCLIArchiveLayoutRejectsMissingAlias(t *testing.T) {
	root := t.TempDir()
	archivePath := writePortableCLITarGz(t, root, "spacedatanetwork-1.2.3-linux-amd64.tar.gz", "spacedatanetwork-1.2.3-linux-amd64", true)

	err := verifyPortableCLIArchiveLayout(archivePath, portableCLITarget{
		Label:       "Linux AMD64 portable CLI",
		PrimaryPath: "bin/spacedatanetwork",
		AliasPath:   "bin/sdn",
		ArchiveKind: "tar.gz",
	})
	if err == nil || !strings.Contains(err.Error(), "bin/sdn") {
		t.Fatalf("verifyPortableCLIArchiveLayout error = %v, want missing bin/sdn", err)
	}
}

func TestVerifyPortableCLIArchiveLayoutRejectsMissingWindowsWasmEdgeRuntime(t *testing.T) {
	root := t.TempDir()
	archivePath := writePortableCLIZipWithoutWindowsWasmEdgeRuntime(t, root, "spacedatanetwork-1.2.3-windows-amd64.zip", "spacedatanetwork-1.2.3-windows-amd64")

	err := verifyPortableCLIArchiveLayout(archivePath, portableCLITarget{
		Label:       "Windows AMD64 portable CLI",
		PrimaryPath: "bin/spacedatanetwork.exe",
		AliasPath:   "bin/sdn.exe",
		ArchiveKind: "zip",
	})
	if err == nil || !strings.Contains(err.Error(), "bin/wasmedge.dll") {
		t.Fatalf("verifyPortableCLIArchiveLayout error = %v, want missing bin/wasmedge.dll", err)
	}
}

func TestVerifyContainerDigestsRejectsSplitFullAndEdgeImages(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "container-digests.json")
	writeReleaseTestJSON(t, root, "container-digests.json", map[string]any{
		"images": []map[string]any{
			{"name": "dockerdigitalarsenal/space-data-network-full", "digest": "sha256:" + strings.Repeat("b", 64)},
			{"name": "dockerdigitalarsenal/space-data-network-edge", "digest": "sha256:" + strings.Repeat("c", 64)},
		},
	})

	err := verifyContainerDigests(path)
	if err == nil || !strings.Contains(err.Error(), "single dockerdigitalarsenal/space-data-network image") {
		t.Fatalf("verifyContainerDigests error = %v, want split image rejection", err)
	}
}

func TestVerifyReleaseDirectoryRejectsMissingBitcoinEvidence(t *testing.T) {
	root := t.TempDir()
	writeReleaseTestFile(t, root, "release.plg", buildTestReleasePLG(t))
	writeReleaseTestFile(t, root, "release.pnm", buildTestReleasePNM(t, "bafybeireleaseplgcid", "bitcoin-proof-ref"))
	writeReleaseTestFile(t, root, "spacedatanetwork-checksums.txt", []byte{})

	if _, err := verifyReleaseDirectory(root); err == nil || !strings.Contains(err.Error(), "release-bitcoin.json") {
		t.Fatalf("verifyReleaseDirectory error = %v, want missing release-bitcoin.json", err)
	}
}

func buildTestReleasePLG(t *testing.T) []byte {
	t.Helper()
	builder := flatbuffers.NewBuilder(256)
	id := builder.CreateString("io.spacedatanetwork.release")
	name := builder.CreateString("Space Data Network release")
	version := builder.CreateString("1.2.3")
	builder.StartObject(3)
	builder.PrependUOffsetTSlot(2, version, 0)
	builder.PrependUOffsetTSlot(1, name, 0)
	builder.PrependUOffsetTSlot(0, id, 0)
	root := builder.EndObject()
	builder.FinishSizePrefixedWithFileIdentifier(root, []byte("$PLG"))
	return append([]byte(nil), builder.FinishedBytes()...)
}

func buildTestReleasePNM(t *testing.T, cid string, proof string) []byte {
	t.Helper()
	builder := flatbuffers.NewBuilder(256)
	addrOffset := builder.CreateString("/ipfs/" + cid)
	timestampOffset := builder.CreateString("2026-04-30T00:00:00Z")
	cidOffset := builder.CreateString(cid)
	fileNameOffset := builder.CreateString("spacedatanetwork-v1.2.3")
	fileIDOffset := builder.CreateString("SDN_RELEASE")
	signatureOffset := builder.CreateString("release-signature")
	timestampSigOffset := builder.CreateString(proof)
	signatureTypeOffset := builder.CreateString("secp256k1-sdn-release")
	timestampSigTypeOffset := builder.CreateString("bitcoin")
	PNM.PNMStart(builder)
	PNM.PNMAddMULTIFORMAT_ADDRESS(builder, addrOffset)
	PNM.PNMAddPUBLISH_TIMESTAMP(builder, timestampOffset)
	PNM.PNMAddCID(builder, cidOffset)
	PNM.PNMAddFILE_NAME(builder, fileNameOffset)
	PNM.PNMAddFILE_ID(builder, fileIDOffset)
	PNM.PNMAddSIGNATURE(builder, signatureOffset)
	PNM.PNMAddTIMESTAMP_SIGNATURE(builder, timestampSigOffset)
	PNM.PNMAddSIGNATURE_TYPE(builder, signatureTypeOffset)
	PNM.PNMAddTIMESTAMP_SIGNATURE_TYPE(builder, timestampSigTypeOffset)
	root := PNM.PNMEnd(builder)
	PNM.FinishSizePrefixedPNMBuffer(builder, root)
	return append([]byte(nil), builder.FinishedBytes()...)
}

func writeReleaseTestJSON(t *testing.T, root string, name string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeReleaseTestFile(t, root, name, data)
}

func writeReleaseTestFile(t *testing.T, root string, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writePortableCLITarGz(t *testing.T, root string, name string, bundleRoot string, omitAlias bool) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	writer := tar.NewWriter(gz)
	entries := portableCLIEntries(bundleRoot, false, omitAlias)
	for pathValue, contents := range entries {
		header := &tar.Header{
			Name: pathValue,
			Mode: 0o755,
			Size: int64(len(contents)),
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(contents)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	writeReleaseTestFile(t, root, name, buf.Bytes())
	return filepath.Join(root, name)
}

func writePortableCLIZip(t *testing.T, root string, name string, bundleRoot string, omitAlias bool) string {
	t.Helper()
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for pathValue, contents := range portableCLIEntries(bundleRoot, true, omitAlias) {
		file, err := writer.Create(pathValue)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte(contents)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	writeReleaseTestFile(t, root, name, buf.Bytes())
	return filepath.Join(root, name)
}

func writePortableCLIZipWithoutWindowsWasmEdgeRuntime(t *testing.T, root string, name string, bundleRoot string) string {
	t.Helper()
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for _, pathValue := range []string{
		bundleRoot + "/bin/spacedatanetwork.exe",
		bundleRoot + "/bin/sdn.exe",
		bundleRoot + "/runtime/modules/org.spacedatanetwork.updater.wasm",
		bundleRoot + "/manifest.json",
	} {
		file, err := writer.Create(pathValue)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte("fixture")); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	writeReleaseTestFile(t, root, name, buf.Bytes())
	return filepath.Join(root, name)
}

func portableCLIEntries(bundleRoot string, windows bool, omitAlias bool) map[string]string {
	primary := "spacedatanetwork"
	alias := "sdn"
	if windows {
		primary += ".exe"
		alias += ".exe"
	}
	entries := map[string]string{
		bundleRoot + "/bin/" + primary:                                    "primary",
		bundleRoot + "/runtime/modules/org.spacedatanetwork.updater.wasm": "updater",
		bundleRoot + "/manifest.json":                                     `{"schema":"org.spacedatanetwork.bundle.v1"}`,
	}
	if windows {
		entries[bundleRoot+"/bin/wasmedge.dll"] = "dll"
		entries[bundleRoot+"/runtime/wasmedge/bin/wasmedge.dll"] = "dll"
	}
	if !omitAlias {
		entries[bundleRoot+"/bin/"+alias] = "alias"
	}
	return entries
}

func writeChecksums(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == "spacedatanetwork-checksums.txt" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		lines = append(lines, hex.EncodeToString(sum[:])+"  "+entry.Name())
	}
	writeReleaseTestFile(t, root, "spacedatanetwork-checksums.txt", []byte(strings.Join(lines, "\n")+"\n"))
}

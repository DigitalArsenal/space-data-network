package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spf13/cobra"
)

var releaseCmd = &cobra.Command{
	Use:   "release",
	Short: "Inspect and verify SDN release artifacts",
}

var releaseVerifyCmd = &cobra.Command{
	Use:   "verify <artifact-dir>",
	Short: "Verify release PLG/PNM, Bitcoin evidence, checksums, and artifacts",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		report, err := verifyReleaseDirectory(args[0])
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	},
}

var releaseCreateRecordsOptions struct {
	Version               string
	ReleasePLGCID         string
	SDNReleasePublicKey   string
	BitcoinSignature      string
	BitcoinPublicKey      string
	BitcoinAddress        string
	BitcoinDescriptor     string
	BitcoinNetwork        string
	BitcoinAnchorMethod   string
	BitcoinTransactionID  string
	BitcoinProofReference string
	BitcoinOutputIndex    string
	BitcoinBlockHeight    string
	BitcoinConfirmations  string
}

var releaseCreateRecordsCmd = &cobra.Command{
	Use:   "create-records <artifact-dir>",
	Short: "Create release.plg, release.pnm, and release-bitcoin.json",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return createReleaseRecords(args[0], releaseCreateRecordsOptions)
	},
}

func init() {
	releaseCreateRecordsCmd.Flags().StringVar(&releaseCreateRecordsOptions.Version, "version", "", "SDN release version")
	releaseCreateRecordsCmd.Flags().StringVar(&releaseCreateRecordsOptions.ReleasePLGCID, "release-plg-cid", "", "IPFS CID for release.plg or release artifact collection")
	releaseCreateRecordsCmd.Flags().StringVar(&releaseCreateRecordsOptions.SDNReleasePublicKey, "release-public-key", "", "compressed secp256k1 SDN release public key")
	releaseCreateRecordsCmd.Flags().StringVar(&releaseCreateRecordsOptions.BitcoinSignature, "bitcoin-signature", "", "Bitcoin signature or proof commitment")
	releaseCreateRecordsCmd.Flags().StringVar(&releaseCreateRecordsOptions.BitcoinPublicKey, "bitcoin-public-key", "", "Bitcoin public key")
	releaseCreateRecordsCmd.Flags().StringVar(&releaseCreateRecordsOptions.BitcoinAddress, "bitcoin-address", "", "Bitcoin address")
	releaseCreateRecordsCmd.Flags().StringVar(&releaseCreateRecordsOptions.BitcoinDescriptor, "bitcoin-descriptor", "", "Bitcoin descriptor")
	releaseCreateRecordsCmd.Flags().StringVar(&releaseCreateRecordsOptions.BitcoinNetwork, "bitcoin-network", "", "Bitcoin network")
	releaseCreateRecordsCmd.Flags().StringVar(&releaseCreateRecordsOptions.BitcoinAnchorMethod, "bitcoin-anchor-method", "", "Bitcoin anchoring method")
	releaseCreateRecordsCmd.Flags().StringVar(&releaseCreateRecordsOptions.BitcoinTransactionID, "bitcoin-txid", "", "Bitcoin transaction ID")
	releaseCreateRecordsCmd.Flags().StringVar(&releaseCreateRecordsOptions.BitcoinProofReference, "bitcoin-proof", "", "OpenTimestamps/Taproot/OP_RETURN proof reference")
	releaseCreateRecordsCmd.Flags().StringVar(&releaseCreateRecordsOptions.BitcoinOutputIndex, "bitcoin-output-index", "", "Bitcoin output index")
	releaseCreateRecordsCmd.Flags().StringVar(&releaseCreateRecordsOptions.BitcoinBlockHeight, "bitcoin-block-height", "", "Bitcoin block height")
	releaseCreateRecordsCmd.Flags().StringVar(&releaseCreateRecordsOptions.BitcoinConfirmations, "bitcoin-confirmations", "", "Bitcoin confirmation count")
	releaseCmd.AddCommand(releaseCreateRecordsCmd)
	releaseCmd.AddCommand(releaseVerifyCmd)
	rootCmd.AddCommand(releaseCmd)
}

type releaseVerificationReport struct {
	OK        bool     `json:"ok"`
	Directory string   `json:"directory"`
	Artifacts []string `json:"artifacts"`
	Warnings  []string `json:"warnings,omitempty"`
}

type releaseBitcoinEvidence struct {
	ReleasePublicationHash   string   `json:"releasePublicationHash"`
	BitcoinSignature         string   `json:"bitcoinSignature"`
	BitcoinPublicKey         string   `json:"bitcoinPublicKey"`
	BitcoinAddress           string   `json:"bitcoinAddress"`
	BitcoinDescriptor        string   `json:"bitcoinDescriptor"`
	Network                  string   `json:"network"`
	AnchorMethod             string   `json:"anchorMethod"`
	TransactionID            string   `json:"transactionId"`
	TimestampProofReference  string   `json:"timestampProofReference"`
	OutputIndex              *uint64  `json:"outputIndex"`
	BlockHeight              *uint64  `json:"blockHeight"`
	Confirmations            *uint64  `json:"confirmations"`
	VerificationInstructions []string `json:"verificationInstructions"`
}

func createReleaseRecords(rawDir string, options struct {
	Version               string
	ReleasePLGCID         string
	SDNReleasePublicKey   string
	BitcoinSignature      string
	BitcoinPublicKey      string
	BitcoinAddress        string
	BitcoinDescriptor     string
	BitcoinNetwork        string
	BitcoinAnchorMethod   string
	BitcoinTransactionID  string
	BitcoinProofReference string
	BitcoinOutputIndex    string
	BitcoinBlockHeight    string
	BitcoinConfirmations  string
}) error {
	dir, err := filepath.Abs(strings.TrimSpace(rawDir))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	version := firstNonEmpty(strings.TrimSpace(options.Version), "0.0.0")
	releaseCID := firstNonEmpty(strings.TrimSpace(options.ReleasePLGCID), "bafybeireleasepending")
	releasePublicKey := strings.TrimSpace(options.SDNReleasePublicKey)
	if releasePublicKey == "" {
		return errors.New("--release-public-key is required")
	}
	plgBytes := buildReleasePLGRecord(version, releasePublicKey)
	if err := os.WriteFile(filepath.Join(dir, "release.plg"), plgBytes, 0o600); err != nil {
		return err
	}
	proofReference := firstNonEmpty(strings.TrimSpace(options.BitcoinProofReference), strings.TrimSpace(options.BitcoinTransactionID), "bitcoin-proof-pending")
	pnmBytes := buildReleasePNMRecord(version, releaseCID, releasePublicKey, proofReference)
	if err := os.WriteFile(filepath.Join(dir, "release.pnm"), pnmBytes, 0o600); err != nil {
		return err
	}
	evidence, err := buildReleaseBitcoinEvidence(plgBytes, options)
	if err != nil {
		return err
	}
	evidenceBytes, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "release-bitcoin.json"), append(evidenceBytes, '\n'), 0o600)
}

func verifyReleaseDirectory(rawDir string) (*releaseVerificationReport, error) {
	dir, err := filepath.Abs(strings.TrimSpace(rawDir))
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("release artifact directory is not accessible: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("release artifact path is not a directory: %s", dir)
	}

	files, err := releaseDirectoryFiles(dir)
	if err != nil {
		return nil, err
	}
	for _, name := range []string{
		"release.plg",
		"release.pnm",
		"release-bitcoin.json",
		"ipfs-deployment.json",
		"spacedatanetwork-sbom.cdx.json",
		"container-digests.json",
		"spacedatanetwork-checksums.txt",
	} {
		if _, ok := files[name]; !ok {
			return nil, fmt.Errorf("required release artifact %s is missing", name)
		}
	}
	if err := requireReleaseArtifactPattern(files, "full-node RPM", func(name string) bool {
		return strings.HasSuffix(name, ".rpm") && strings.Contains(name, "full")
	}); err != nil {
		return nil, err
	}
	if err := requireReleaseArtifactPattern(files, "edge-relay RPM", func(name string) bool {
		return strings.HasSuffix(name, ".rpm") && strings.Contains(name, "edge")
	}); err != nil {
		return nil, err
	}
	if err := requireReleaseArtifactPattern(files, "full-node DEB", func(name string) bool {
		return strings.HasSuffix(name, ".deb") && strings.Contains(name, "full")
	}); err != nil {
		return nil, err
	}
	if err := requireReleaseArtifactPattern(files, "edge-relay DEB", func(name string) bool {
		return strings.HasSuffix(name, ".deb") && strings.Contains(name, "edge")
	}); err != nil {
		return nil, err
	}
	if err := requireReleaseArtifactPattern(files, "Linux VM tarball", func(name string) bool {
		return strings.HasSuffix(name, ".tar.gz") && strings.Contains(name, "linux-vm")
	}); err != nil {
		return nil, err
	}
	if err := verifyPortableCLIArtifacts(dir, files); err != nil {
		return nil, err
	}

	releasePLGBytes, err := os.ReadFile(filepath.Join(dir, "release.plg"))
	if err != nil {
		return nil, err
	}
	if err := verifyReleasePLG(releasePLGBytes); err != nil {
		return nil, err
	}
	if err := verifyReleasePNM(filepath.Join(dir, "release.pnm")); err != nil {
		return nil, err
	}
	if err := verifyIPFSDeployment(filepath.Join(dir, "ipfs-deployment.json")); err != nil {
		return nil, err
	}
	if err := verifyContainerDigests(filepath.Join(dir, "container-digests.json")); err != nil {
		return nil, err
	}
	if err := verifyReleaseSBOM(filepath.Join(dir, "spacedatanetwork-sbom.cdx.json")); err != nil {
		return nil, err
	}
	if err := verifyReleaseBitcoinEvidence(filepath.Join(dir, "release-bitcoin.json"), releasePLGBytes); err != nil {
		return nil, err
	}
	if err := verifyReleaseChecksums(dir, files); err != nil {
		return nil, err
	}

	return &releaseVerificationReport{
		OK:        true,
		Directory: dir,
		Artifacts: sortedReleaseFileNames(files),
	}, nil
}

func releaseDirectoryFiles(dir string) (map[string]os.DirEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read release artifact directory: %w", err)
	}
	files := make(map[string]os.DirEntry, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		files[entry.Name()] = entry
	}
	return files, nil
}

func requireReleaseArtifactPattern(files map[string]os.DirEntry, label string, match func(string) bool) error {
	for name := range files {
		if match(name) {
			return nil
		}
	}
	return fmt.Errorf("required release artifact is missing: %s", label)
}

type portableCLITarget struct {
	Label       string
	Suffix      string
	PrimaryPath string
	AliasPath   string
	ArchiveKind string
}

func verifyPortableCLIArtifacts(dir string, files map[string]os.DirEntry) error {
	targets := []portableCLITarget{
		{Label: "macOS ARM64 portable CLI", Suffix: "-darwin-arm64.tar.gz", PrimaryPath: "bin/spacedatanetwork", AliasPath: "bin/sdn", ArchiveKind: "tar.gz"},
		{Label: "macOS AMD64 portable CLI", Suffix: "-darwin-amd64.tar.gz", PrimaryPath: "bin/spacedatanetwork", AliasPath: "bin/sdn", ArchiveKind: "tar.gz"},
		{Label: "Linux AMD64 portable CLI", Suffix: "-linux-amd64.tar.gz", PrimaryPath: "bin/spacedatanetwork", AliasPath: "bin/sdn", ArchiveKind: "tar.gz"},
		{Label: "Linux ARM64 portable CLI", Suffix: "-linux-arm64.tar.gz", PrimaryPath: "bin/spacedatanetwork", AliasPath: "bin/sdn", ArchiveKind: "tar.gz"},
		{Label: "Windows AMD64 portable CLI", Suffix: "-windows-amd64.zip", PrimaryPath: "bin/spacedatanetwork.exe", AliasPath: "bin/sdn.exe", ArchiveKind: "zip"},
	}
	for _, target := range targets {
		name := portableCLIArtifactName(files, target.Suffix)
		if name == "" {
			return fmt.Errorf("required release artifact is missing: %s", target.Label)
		}
		if err := verifyPortableCLIArchiveLayout(filepath.Join(dir, name), target); err != nil {
			return fmt.Errorf("verify %s: %w", name, err)
		}
	}
	return nil
}

func portableCLIArtifactName(files map[string]os.DirEntry, suffix string) string {
	for name := range files {
		if strings.HasPrefix(name, "spacedatanetwork-") && strings.HasSuffix(name, suffix) && !strings.Contains(name, "linux-vm") {
			return name
		}
	}
	return ""
}

func verifyPortableCLIArchiveLayout(pathValue string, target portableCLITarget) error {
	var entries map[string]bool
	var err error
	switch target.ArchiveKind {
	case "tar.gz":
		entries, err = tarGzEntries(pathValue)
	case "zip":
		entries, err = zipEntries(pathValue)
	default:
		err = fmt.Errorf("unsupported archive kind %q", target.ArchiveKind)
	}
	if err != nil {
		return err
	}
	for _, required := range []string{
		target.PrimaryPath,
		target.AliasPath,
		"runtime/modules/org.spacedatanetwork.updater.wasm",
		"manifest.json",
	} {
		if !archiveContainsRelativePath(entries, required) {
			return fmt.Errorf("portable CLI archive missing %s", required)
		}
	}
	return nil
}

func tarGzEntries(pathValue string) (map[string]bool, error) {
	file, err := os.Open(pathValue)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	entries := map[string]bool{}
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return entries, nil
		}
		if err != nil {
			return nil, err
		}
		entries[normalizeArchivePath(header.Name)] = true
	}
}

func zipEntries(pathValue string) (map[string]bool, error) {
	reader, err := zip.OpenReader(pathValue)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	entries := map[string]bool{}
	for _, file := range reader.File {
		entries[normalizeArchivePath(file.Name)] = true
	}
	return entries, nil
}

func archiveContainsRelativePath(entries map[string]bool, required string) bool {
	required = normalizeArchivePath(required)
	for entry := range entries {
		if entry == required || strings.HasSuffix(entry, "/"+required) {
			return true
		}
	}
	return false
}

func normalizeArchivePath(value string) string {
	value = filepath.ToSlash(filepath.Clean(strings.TrimSpace(value)))
	value = strings.TrimPrefix(value, "./")
	return strings.Trim(value, "/")
}

func verifyReleasePLG(data []byte) error {
	if len(data) == 0 {
		return errors.New("release.plg is empty")
	}
	if flatbuffers.SizePrefixedBufferHasIdentifier(data, "$PLG") || flatbuffers.BufferHasIdentifier(data, "$PLG") {
		return nil
	}
	return errors.New("release.plg is not an SDS PLG FlatBuffer")
}

func verifyReleasePNM(pathValue string) error {
	data, err := os.ReadFile(pathValue)
	if err != nil {
		return fmt.Errorf("read release.pnm: %w", err)
	}
	if len(data) == 0 {
		return errors.New("release.pnm is empty")
	}
	var pnm *PNM.PNM
	switch {
	case PNM.SizePrefixedPNMBufferHasIdentifier(data):
		pnm = PNM.GetSizePrefixedRootAsPNM(data, 0)
	case PNM.PNMBufferHasIdentifier(data):
		pnm = PNM.GetRootAsPNM(data, 0)
	default:
		return errors.New("release.pnm is not an SDS PNM FlatBuffer")
	}
	if strings.TrimSpace(string(pnm.CID())) == "" {
		return errors.New("release.pnm missing CID")
	}
	if got := strings.TrimSpace(string(pnm.FILE_ID())); got != "SDN_RELEASE" {
		return fmt.Errorf("release.pnm FILE_ID = %q, want SDN_RELEASE", got)
	}
	if strings.TrimSpace(string(pnm.SIGNATURE())) == "" {
		return errors.New("release.pnm missing SDN release signature")
	}
	if strings.ToLower(strings.TrimSpace(string(pnm.TIMESTAMP_SIGNATURE_TYPE()))) != "bitcoin" {
		return errors.New("release.pnm TIMESTAMP_SIGNATURE_TYPE must be bitcoin")
	}
	if strings.TrimSpace(string(pnm.TIMESTAMP_SIGNATURE())) == "" {
		return errors.New("release.pnm missing Bitcoin timestamp signature/proof reference")
	}
	return nil
}

func verifyIPFSDeployment(pathValue string) error {
	var payload map[string]interface{}
	if err := readJSONFile(pathValue, &payload); err != nil {
		return fmt.Errorf("verify ipfs-deployment.json: %w", err)
	}
	if !jsonContainsCID(payload, "sdnAdmin") && !jsonContainsCID(payload, "sdnUI") {
		return errors.New("ipfs-deployment.json missing SDN admin/UI CID")
	}
	if !jsonContainsCID(payload, "ipfsWebui") && !jsonContainsCID(payload, "webui") {
		return errors.New("ipfs-deployment.json missing IPFS WebUI CID")
	}
	return nil
}

func verifyContainerDigests(pathValue string) error {
	var payload struct {
		Images []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
		} `json:"images"`
	}
	if err := readJSONFile(pathValue, &payload); err != nil {
		return fmt.Errorf("verify container-digests.json: %w", err)
	}
	var found bool
	for _, image := range payload.Images {
		if !validSHA256Digest(image.Digest) {
			return fmt.Errorf("container image %q has invalid digest %q", image.Name, image.Digest)
		}
		lowerName := strings.ToLower(image.Name)
		if lowerName == "dockerdigitalarsenal/space-data-network" || lowerName == "docker.io/dockerdigitalarsenal/space-data-network" {
			found = true
			continue
		}
		if strings.Contains(lowerName, "space-data-network-full") || strings.Contains(lowerName, "space-data-network-edge") {
			return errors.New("container-digests.json must include a single dockerdigitalarsenal/space-data-network image, not split full and edge images")
		}
	}
	if !found {
		return errors.New("container-digests.json must include a single dockerdigitalarsenal/space-data-network image")
	}
	return nil
}

func verifyReleaseSBOM(pathValue string) error {
	var payload struct {
		BOMFormat string `json:"bomFormat"`
	}
	if err := readJSONFile(pathValue, &payload); err != nil {
		return fmt.Errorf("verify spacedatanetwork-sbom.cdx.json: %w", err)
	}
	if strings.ToLower(strings.TrimSpace(payload.BOMFormat)) != "cyclonedx" {
		return errors.New("spacedatanetwork-sbom.cdx.json must be a CycloneDX SBOM")
	}
	return nil
}

func verifyReleaseBitcoinEvidence(pathValue string, releasePLGBytes []byte) error {
	var evidence releaseBitcoinEvidence
	if err := readJSONFile(pathValue, &evidence); err != nil {
		return fmt.Errorf("verify release-bitcoin.json: %w", err)
	}
	releaseHash := sha256.Sum256(releasePLGBytes)
	wantHash := hex.EncodeToString(releaseHash[:])
	gotHash := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(evidence.ReleasePublicationHash)), "sha256:")
	if gotHash != wantHash {
		return fmt.Errorf("release-bitcoin.json releasePublicationHash = %q, want sha256:%s", evidence.ReleasePublicationHash, wantHash)
	}
	if strings.TrimSpace(evidence.BitcoinSignature) == "" {
		return errors.New("release-bitcoin.json missing bitcoinSignature")
	}
	if strings.TrimSpace(evidence.BitcoinPublicKey) == "" &&
		strings.TrimSpace(evidence.BitcoinAddress) == "" &&
		strings.TrimSpace(evidence.BitcoinDescriptor) == "" {
		return errors.New("release-bitcoin.json must include bitcoinPublicKey, bitcoinAddress, or bitcoinDescriptor")
	}
	if strings.TrimSpace(evidence.Network) == "" {
		return errors.New("release-bitcoin.json missing network")
	}
	if strings.TrimSpace(evidence.TransactionID) == "" && strings.TrimSpace(evidence.TimestampProofReference) == "" {
		return errors.New("release-bitcoin.json missing transactionId or timestampProofReference")
	}
	if strings.TrimSpace(evidence.AnchorMethod) == "" {
		return errors.New("release-bitcoin.json missing anchorMethod")
	}
	if len(evidence.VerificationInstructions) == 0 {
		return errors.New("release-bitcoin.json missing verificationInstructions")
	}
	if evidence.TransactionID != "" && !isHexString(evidence.TransactionID, 64) {
		return fmt.Errorf("release-bitcoin.json transactionId is not a 64-character hex string")
	}
	return nil
}

func verifyReleaseChecksums(dir string, files map[string]os.DirEntry) error {
	checksumPath := filepath.Join(dir, "spacedatanetwork-checksums.txt")
	file, err := os.Open(checksumPath)
	if err != nil {
		return fmt.Errorf("open spacedatanetwork-checksums.txt: %w", err)
	}
	defer file.Close()

	seen := map[string]bool{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			return fmt.Errorf("invalid checksum line %q", line)
		}
		expected := strings.ToLower(parts[0])
		name := strings.Join(parts[1:], " ")
		name = strings.TrimPrefix(name, "*")
		name = filepath.Clean(name)
		if filepath.IsAbs(name) || strings.HasPrefix(name, "..") {
			return fmt.Errorf("invalid checksum path %q", name)
		}
		entry, ok := files[name]
		if !ok || entry.IsDir() {
			return fmt.Errorf("checksum references missing artifact %q", name)
		}
		if !isHexString(expected, 64) {
			return fmt.Errorf("checksum for %q is not a SHA-256 hex digest", name)
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("read checksum artifact %q: %w", name, err)
		}
		actual := sha256.Sum256(data)
		if hex.EncodeToString(actual[:]) != expected {
			return fmt.Errorf("checksum mismatch for %q", name)
		}
		seen[name] = true
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read spacedatanetwork-checksums.txt: %w", err)
	}
	for name := range files {
		if name == "spacedatanetwork-checksums.txt" {
			continue
		}
		if !seen[name] {
			return fmt.Errorf("artifact %q missing from spacedatanetwork-checksums.txt", name)
		}
	}
	return nil
}

func readJSONFile(pathValue string, target interface{}) error {
	data, err := os.ReadFile(pathValue)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return fmt.Errorf("%s is empty", filepath.Base(pathValue))
	}
	if err := json.Unmarshal(data, target); err != nil {
		return err
	}
	return nil
}

func jsonContainsCID(payload map[string]interface{}, key string) bool {
	value, ok := payload[key]
	if !ok {
		return false
	}
	record, ok := value.(map[string]interface{})
	if !ok {
		return false
	}
	cid, _ := record["cid"].(string)
	cid = strings.TrimSpace(cid)
	return strings.HasPrefix(cid, "bafy") || strings.HasPrefix(cid, "Qm")
}

func validSHA256Digest(value string) bool {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "sha256:") {
		return false
	}
	return isHexString(strings.TrimPrefix(value, "sha256:"), 64)
}

func isHexString(value string, length int) bool {
	if len(value) != length {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func sortedReleaseFileNames(files map[string]os.DirEntry) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	return names
}

func buildReleasePLGRecord(version, releasePublicKey string) []byte {
	builder := flatbuffers.NewBuilder(512)
	pluginIDOffset := builder.CreateString("io.spacedatanetwork.release")
	nameOffset := builder.CreateString("Space Data Network release")
	versionOffset := builder.CreateString(version)
	descriptionOffset := builder.CreateString("Signed SDN binary release publication record; release key " + releasePublicKey)
	builder.StartObject(4)
	builder.PrependUOffsetTSlot(3, descriptionOffset, 0)
	builder.PrependUOffsetTSlot(2, versionOffset, 0)
	builder.PrependUOffsetTSlot(1, nameOffset, 0)
	builder.PrependUOffsetTSlot(0, pluginIDOffset, 0)
	root := builder.EndObject()
	builder.FinishSizePrefixedWithFileIdentifier(root, []byte("$PLG"))
	return append([]byte(nil), builder.FinishedBytes()...)
}

func buildReleasePNMRecord(version, releaseCID, releasePublicKey, proofReference string) []byte {
	builder := flatbuffers.NewBuilder(512)
	multiformatAddressOffset := builder.CreateString("/ipfs/" + releaseCID)
	timestampOffset := builder.CreateString(time.Now().UTC().Format(time.RFC3339))
	cidOffset := builder.CreateString(releaseCID)
	fileNameOffset := builder.CreateString("spacedatanetwork-v" + version)
	fileIDOffset := builder.CreateString("SDN_RELEASE")
	signatureOffset := builder.CreateString("release-key:" + releasePublicKey)
	timestampSignatureOffset := builder.CreateString(proofReference)
	signatureTypeOffset := builder.CreateString("secp256k1-sdn-release")
	timestampSignatureTypeOffset := builder.CreateString("bitcoin")
	PNM.PNMStart(builder)
	PNM.PNMAddMULTIFORMAT_ADDRESS(builder, multiformatAddressOffset)
	PNM.PNMAddPUBLISH_TIMESTAMP(builder, timestampOffset)
	PNM.PNMAddCID(builder, cidOffset)
	PNM.PNMAddFILE_NAME(builder, fileNameOffset)
	PNM.PNMAddFILE_ID(builder, fileIDOffset)
	PNM.PNMAddSIGNATURE(builder, signatureOffset)
	PNM.PNMAddTIMESTAMP_SIGNATURE(builder, timestampSignatureOffset)
	PNM.PNMAddSIGNATURE_TYPE(builder, signatureTypeOffset)
	PNM.PNMAddTIMESTAMP_SIGNATURE_TYPE(builder, timestampSignatureTypeOffset)
	root := PNM.PNMEnd(builder)
	PNM.FinishSizePrefixedPNMBuffer(builder, root)
	return append([]byte(nil), builder.FinishedBytes()...)
}

func buildReleaseBitcoinEvidence(plgBytes []byte, options struct {
	Version               string
	ReleasePLGCID         string
	SDNReleasePublicKey   string
	BitcoinSignature      string
	BitcoinPublicKey      string
	BitcoinAddress        string
	BitcoinDescriptor     string
	BitcoinNetwork        string
	BitcoinAnchorMethod   string
	BitcoinTransactionID  string
	BitcoinProofReference string
	BitcoinOutputIndex    string
	BitcoinBlockHeight    string
	BitcoinConfirmations  string
}) (releaseBitcoinEvidence, error) {
	signature := strings.TrimSpace(options.BitcoinSignature)
	if signature == "" {
		return releaseBitcoinEvidence{}, errors.New("--bitcoin-signature is required")
	}
	if strings.TrimSpace(options.BitcoinPublicKey) == "" &&
		strings.TrimSpace(options.BitcoinAddress) == "" &&
		strings.TrimSpace(options.BitcoinDescriptor) == "" {
		return releaseBitcoinEvidence{}, errors.New("one of --bitcoin-public-key, --bitcoin-address, or --bitcoin-descriptor is required")
	}
	if strings.TrimSpace(options.BitcoinNetwork) == "" {
		return releaseBitcoinEvidence{}, errors.New("--bitcoin-network is required")
	}
	if strings.TrimSpace(options.BitcoinTransactionID) == "" && strings.TrimSpace(options.BitcoinProofReference) == "" {
		return releaseBitcoinEvidence{}, errors.New("--bitcoin-txid or --bitcoin-proof is required")
	}
	if strings.TrimSpace(options.BitcoinAnchorMethod) == "" {
		return releaseBitcoinEvidence{}, errors.New("--bitcoin-anchor-method is required")
	}
	releaseHash := sha256.Sum256(plgBytes)
	evidence := releaseBitcoinEvidence{
		ReleasePublicationHash:  "sha256:" + hex.EncodeToString(releaseHash[:]),
		BitcoinSignature:        signature,
		BitcoinPublicKey:        strings.TrimSpace(options.BitcoinPublicKey),
		BitcoinAddress:          strings.TrimSpace(options.BitcoinAddress),
		BitcoinDescriptor:       strings.TrimSpace(options.BitcoinDescriptor),
		Network:                 strings.TrimSpace(options.BitcoinNetwork),
		AnchorMethod:            strings.TrimSpace(options.BitcoinAnchorMethod),
		TransactionID:           strings.TrimSpace(options.BitcoinTransactionID),
		TimestampProofReference: strings.TrimSpace(options.BitcoinProofReference),
		VerificationInstructions: []string{
			"sha256sum -c spacedatanetwork-checksums.txt",
			"spacedatanetwork release verify <artifact-dir>",
			"verify the Bitcoin proof commits to releasePublicationHash",
		},
	}
	if parsed, ok, err := parseOptionalUint64(options.BitcoinOutputIndex); err != nil {
		return releaseBitcoinEvidence{}, fmt.Errorf("--bitcoin-output-index: %w", err)
	} else if ok {
		evidence.OutputIndex = &parsed
	}
	if parsed, ok, err := parseOptionalUint64(options.BitcoinBlockHeight); err != nil {
		return releaseBitcoinEvidence{}, fmt.Errorf("--bitcoin-block-height: %w", err)
	} else if ok {
		evidence.BlockHeight = &parsed
	}
	if parsed, ok, err := parseOptionalUint64(options.BitcoinConfirmations); err != nil {
		return releaseBitcoinEvidence{}, fmt.Errorf("--bitcoin-confirmations: %w", err)
	} else if ok {
		evidence.Confirmations = &parsed
	}
	return evidence, nil
}

func parseOptionalUint64(value string) (uint64, bool, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false, nil
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, false, err
	}
	return parsed, true, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

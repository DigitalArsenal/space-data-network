package update

// UI PACKAGES: the dashboard and homepage a node serves, delivered through
// this lane without a new binary.
//
// OWNER 2026-10-07: "package the updates and send them through the update
// channel, digitally signed and encrypted, and have the running servers update
// in situ WITHOUT needing to republish the binaries."
//
// A UI package is an ordinary update (same schema, same trust roots, same feed,
// same signal topic) with:
//
//   - target {kind: "ui-bundle", platform: "any", arch: "any"};
//   - modules[]: every file it carries, under ui/, with its sha256 (G4);
//   - compatibility.bundle_versions: the binary builds it was made for;
//   - envelope: the payload encrypted once (AES-256-GCM), the content key
//     wrapped for each fleet node's sealed-transport key (G2), so the carrier
//     on the public feed is ciphertext only those nodes can open.
//
// It installs while the daemon serves, because nothing about it needs a
// restart: the verified files land in updates/ui/<sequence>/ and
// updates/ui/current.json flips to them in one rename, which the daemon sees
// on its next request. The binary's embedded UI is the floor. A package serves
// only on a build it names, so the next binary, whose embedded UI already
// carries the change, retires it without anyone deleting anything.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// TargetKindUIBundle is the target.kind of a UI package.
	TargetKindUIBundle = "ui-bundle"
	// TargetAny is a UI package's platform and arch: the same files serve on
	// every build.
	TargetAny = "any"

	uiDirName       = "ui"
	uiCurrentName   = "current.json"
	uiPackageSchema = "org.spacedatanetwork.update.ui.v1"
	// uiKeep is how many installed packages stay on disk: the current one and
	// the two before it, so a bad package can be stepped back by hand.
	uiKeep = 3
)

// uiRequiredFiles are the files every UI package carries, relative to ui/.
var uiRequiredFiles = []string{"dashboard.html", "dashboard.csp", "homepage.html", "homepage.csp"}

// uiMediaExtensions are the media a package may carry under ui/media/.
var uiMediaExtensions = map[string]bool{".mp4": true, ".jpg": true, ".png": true, ".webp": true}

// uiFilePath reports whether a modules[] path names a file a UI package may
// carry, and returns it relative to ui/.
func uiFilePath(p string) (string, bool) {
	rel, ok := strings.CutPrefix(path.Clean(filepath.ToSlash(p)), uiDirName+"/")
	if !ok {
		return "", false
	}
	for _, name := range uiRequiredFiles {
		if rel == name {
			return rel, true
		}
	}
	name, ok := strings.CutPrefix(rel, "media/")
	if ok && name != "" && !strings.Contains(name, "/") && !strings.HasPrefix(name, ".") &&
		uiMediaExtensions[strings.ToLower(path.Ext(name))] {
		return rel, true
	}
	return "", false
}

// assertUIPackageShape holds the rules only a UI package has: it names every
// file it carries, carries a whole UI, says which builds it is for, and is
// encrypted.
func (m *Manifest) assertUIPackageShape() error {
	if len(m.Modules) == 0 {
		return errors.New("a UI package names its files in modules")
	}
	have := make(map[string]bool, len(m.Modules))
	for _, module := range m.Modules {
		rel, ok := uiFilePath(module.Path)
		if !ok {
			return fmt.Errorf("UI package file %q is not a dashboard, homepage or media file under %s/", module.Path, uiDirName)
		}
		have[rel] = true
	}
	for _, name := range uiRequiredFiles {
		if !have[name] {
			return fmt.Errorf("UI package is missing %s/%s", uiDirName, name)
		}
	}
	if m.Compatibility == nil || len(m.Compatibility.BundleVersions) == 0 {
		return errors.New("a UI package names the builds it was made for (compatibility.bundle_versions)")
	}
	if m.Envelope == nil || len(m.Envelope.Recipients) == 0 {
		return errors.New("a UI package is encrypted to the nodes that install it (envelope)")
	}
	if m.Envelope.Schema != EnvelopeSchemaV1 {
		return fmt.Errorf("unsupported update envelope schema %q", m.Envelope.Schema)
	}
	return nil
}

// ServesOn reports whether the manifest names the build bundleVersion.
func (m *Manifest) ServesOn(bundleVersion string) bool {
	if m.Compatibility == nil {
		return false
	}
	return containsVersion(m.Compatibility.BundleVersions, bundleVersion)
}

func containsVersion(versions []string, version string) bool {
	if strings.TrimSpace(version) == "" {
		return false
	}
	for _, v := range versions {
		if v == version {
			return true
		}
	}
	return false
}

// AddressedTo reports whether the envelope carries a row for keyID, so a node
// the package was not sealed for stops before downloading it.
func (m *Manifest) AddressedTo(keyID []byte) bool {
	if m.Envelope == nil {
		return false
	}
	for _, row := range m.Envelope.Recipients {
		if bytes.Equal(row.KeyID, keyID) {
			return true
		}
	}
	return false
}

// EnvelopeKey opens the envelope row addressed to this node: KeyID is the
// fingerprint the publisher addressed it by, Private the node's
// sealed-transport encryption key.
type EnvelopeKey struct {
	KeyID   []byte
	Private []byte
}

// EnvelopeContext is the ECIES context a package's content key is wrapped
// under. It names the update, so a wrapped key cannot be lifted into another
// package.
func EnvelopeContext(updateID string) string {
	return "sdn-update-envelope-v1:" + updateID
}

// SealPayload encrypts bundle for recipients and records the envelope in the
// unsigned manifest document; it returns the ciphertext carrier to publish as
// update.wasm. manifestDoc is the generic JSON object, so fields this package
// does not model survive, and must already describe the plaintext bundle.
func SealPayload(manifestDoc map[string]any, bundle []byte, recipients []EnvelopeRecipient) ([]byte, error) {
	updateID, _ := manifestDoc["update_id"].(string)
	if strings.TrimSpace(updateID) == "" {
		return nil, errors.New("manifest has no update_id")
	}
	bundleDoc, _ := manifestDoc["bundle"].(map[string]any)
	if bundleDoc == nil {
		return nil, errors.New("manifest has no bundle")
	}
	if hash, _ := bundleDoc["hash"].(string); hash != sha256Hex(bundle) {
		return nil, errors.New("manifest bundle hash does not describe this bundle")
	}
	if size, ok := jsonInt(bundleDoc["size"]); !ok || size != int64(len(bundle)) {
		return nil, errors.New("manifest bundle size does not describe this bundle")
	}
	sealed, err := EncryptCarrierForRecipients(bundle, recipients, EnvelopeContext(updateID))
	if err != nil {
		return nil, err
	}
	manifestDoc["envelope"] = ManifestEnvelope{Schema: sealed.Schema, Context: sealed.Context, Recipients: sealed.Envelopes}
	manifestDoc["wasm"] = map[string]any{"hash": sha256Hex(sealed.Carrier)}
	return sealed.Carrier, nil
}

// jsonInt reads an integer from a generic JSON value, decoded with or
// without UseNumber.
func jsonInt(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), float64(int64(n)) == n
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	}
	return 0, false
}

// OpenPayload returns the plaintext bundle inside a fetched carrier. The
// carrier is checked against the signed manifest before anything is
// decrypted, so only ciphertext the publisher signed is ever opened.
func OpenPayload(m *Manifest, carrier []byte, key *EnvelopeKey) ([]byte, error) {
	if m.Wasm.Hash != sha256Hex(carrier) {
		return nil, errors.New("update wasm hash mismatch")
	}
	if m.Envelope == nil {
		return ExtractBundleFromCarrier(carrier)
	}
	if key == nil || len(key.Private) == 0 {
		return nil, errors.New("this update is encrypted and this node has no key to open it")
	}
	plain, err := DecryptCarrierForRecipient(&EncryptedBundle{
		Schema:    m.Envelope.Schema,
		Context:   m.Envelope.Context,
		Envelopes: m.Envelope.Recipients,
		Carrier:   carrier,
	}, key.KeyID, key.Private)
	if err != nil {
		return nil, err
	}
	return ExtractBundleFromCarrier(plain)
}

// UIPackage is an installed UI package, as updates/ui/current.json records it.
type UIPackage struct {
	Schema         string   `json:"schema"`
	UpdateID       string   `json:"update_id"`
	Version        string   `json:"version"`
	Sequence       int64    `json:"sequence"`
	Channel        string   `json:"channel"`
	BundleVersions []string `json:"bundle_versions"`
	// Dir is the package's directory under updates/ui/.
	Dir string `json:"dir"`
	// Files maps each file, relative to Dir, to the sha256 verified at install.
	Files       map[string]string `json:"files"`
	InstalledAt string            `json:"installed_at"`
}

// ServesOn reports whether the package was made for the build bundleVersion.
func (p *UIPackage) ServesOn(bundleVersion string) bool {
	return p != nil && containsVersion(p.BundleVersions, bundleVersion)
}

// UIRoot is where installed UI packages live.
func UIRoot(paths Paths) string { return filepath.Join(paths.Updates, uiDirName) }

// UICurrentPath is the file whose replacement switches the served UI.
func UICurrentPath(paths Paths) string { return filepath.Join(UIRoot(paths), uiCurrentName) }

// ActiveUIPackage returns the package current.json names, or nil when none
// was ever installed.
func ActiveUIPackage(paths Paths) (*UIPackage, error) {
	data, err := os.ReadFile(UICurrentPath(paths))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var pkg UIPackage
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil, fmt.Errorf("read %s: %w", uiCurrentName, err)
	}
	if pkg.Schema != uiPackageSchema || !isPackageDirName(pkg.Dir) {
		return nil, fmt.Errorf("%s does not describe an installed UI package", uiCurrentName)
	}
	return &pkg, nil
}

// ReadFile reads one of the package's files and checks it against the hash
// verified at install, so a file changed on disk is never served.
func (p *UIPackage) ReadFile(paths Paths, rel string) ([]byte, error) {
	want, ok := p.Files[rel]
	if !ok {
		return nil, fs.ErrNotExist
	}
	data, err := os.ReadFile(filepath.Join(UIRoot(paths), p.Dir, filepath.FromSlash(rel)))
	if err != nil {
		return nil, err
	}
	if sha256Hex(data) != want {
		return nil, fmt.Errorf("installed UI file %s changed on disk after it was verified", rel)
	}
	return data, nil
}

// UIInstall is one fetched UI package and how to judge it.
type UIInstall struct {
	Manifest []byte
	Carrier  []byte
	// Verify gates the manifest. CurrentSequence is the installed UI
	// package's sequence and RunningBundleVersion this build's version.
	Verify VerifyOptions
	// Key opens this node's envelope row.
	Key *EnvelopeKey
	// Trigger and SignalKeyID go into the deploy ledger line written before
	// the switch: "signal" for a pushed package, "operator" for a hand install.
	Trigger     string
	SignalKeyID string
}

// InstallUIPackage verifies a fetched UI package end to end and makes it the
// UI this node serves, while it serves. Everything is checked before a byte
// is decrypted (signature, target, expiry, sequence against the installed
// package, the build it is for), then the carrier against the signed hash,
// then the decrypted bundle and every file in it against theirs. Only then
// is the change ledgered and current.json moved, in one rename.
func InstallUIPackage(paths Paths, in UIInstall) (*UIPackage, error) {
	manifest, err := ParseManifest(in.Manifest)
	if err != nil {
		return nil, err
	}
	if !manifest.IsUIPackage() {
		return nil, fmt.Errorf("%s is not a UI package", manifest.UpdateID)
	}
	if _, err := manifest.Validate(manifest.Bundle.Hash, in.Verify); err != nil {
		return nil, err
	}
	bundle, err := OpenPayload(manifest, in.Carrier, in.Key)
	if err != nil {
		return nil, err
	}
	result, err := manifest.VerifyPayload(in.Carrier, bundle, in.Verify)
	if err != nil {
		return nil, err
	}

	root := UIRoot(paths)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	work, err := os.MkdirTemp(root, ".incoming-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	archiveName, err := bundleFileName(manifest.Bundle.Format)
	if err != nil {
		return nil, err
	}
	archive := filepath.Join(work, archiveName)
	if err := os.WriteFile(archive, bundle, 0o644); err != nil {
		return nil, err
	}
	extracted := filepath.Join(work, "bundle")
	if err := extractBundleArchive(archive, manifest.Bundle.Format, extracted); err != nil {
		return nil, fmt.Errorf("extract UI package: %w", err)
	}
	bundleRoot, err := locateBundleRoot(extracted)
	if err != nil {
		return nil, err
	}
	if err := validateIncomingBundle(bundleRoot, result.Version); err != nil {
		return nil, err
	}
	if _, err := verifyModuleTargets(bundleRoot, manifest.Modules); err != nil {
		return nil, err
	}
	files := make(map[string]string, len(manifest.Modules))
	for _, module := range manifest.Modules {
		rel, _ := uiFilePath(module.Path)
		files[rel] = strings.ToLower(module.Hash)
	}
	if err := assertOnlyUIFiles(filepath.Join(bundleRoot, uiDirName), files); err != nil {
		return nil, err
	}

	dir := strconv.FormatInt(result.Sequence, 10)
	dest := filepath.Join(root, dir)
	if err := os.RemoveAll(dest); err != nil {
		return nil, err
	}
	if err := os.Rename(filepath.Join(bundleRoot, uiDirName), dest); err != nil {
		return nil, err
	}
	pkg := &UIPackage{
		Schema:         uiPackageSchema,
		UpdateID:       manifest.UpdateID,
		Version:        result.Version,
		Sequence:       result.Sequence,
		Channel:        result.Channel,
		BundleVersions: append([]string(nil), manifest.Compatibility.BundleVersions...),
		Dir:            dir,
		Files:          files,
		InstalledAt:    time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.MarshalIndent(pkg, "", "  ")
	if err != nil {
		return nil, err
	}
	// The ledger line goes in before the switch, and a switch that cannot be
	// recorded does not happen (RecordDeployLedgerEntry's contract).
	entry := DeployLedgerEntry{
		Action:      "ui-install",
		UpdateID:    pkg.UpdateID,
		Version:     pkg.Version,
		Sequence:    pkg.Sequence,
		Channel:     pkg.Channel,
		Trigger:     in.Trigger,
		SignalKeyID: in.SignalKeyID,
	}
	if from, err := ActiveUIPackage(paths); err == nil && from != nil {
		entry.FromVersion = from.Version
		entry.FromSequence = from.Sequence
	}
	if err := RecordDeployLedgerEntry(paths, entry); err != nil {
		_ = os.RemoveAll(dest)
		return nil, err
	}
	tmp := UICurrentPath(paths) + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, UICurrentPath(paths)); err != nil {
		return nil, err
	}
	pruneUIPackages(root, dir)
	return pkg, nil
}

// assertOnlyUIFiles refuses a package whose ui/ tree holds anything its
// signed manifest does not name.
func assertOnlyUIFiles(uiDir string, files map[string]string) error {
	return filepath.WalkDir(uiDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(uiDir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == "." || rel == "media" {
				return nil
			}
			return fmt.Errorf("UI package carries an unexpected directory %s/%s", uiDirName, rel)
		}
		if _, ok := files[rel]; !ok || !d.Type().IsRegular() {
			return fmt.Errorf("UI package carries %s/%s, which its manifest does not name", uiDirName, rel)
		}
		return nil
	})
}

func isPackageDirName(name string) bool {
	n, err := strconv.ParseInt(name, 10, 64)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == name
}

// pruneUIPackages keeps the newest uiKeep packages (always the current one)
// and removes the rest, with any leftover incoming directory.
func pruneUIPackages(root, current string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	var dirs []int64
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if strings.HasPrefix(entry.Name(), ".incoming-") {
			if info, err := entry.Info(); err == nil && time.Since(info.ModTime()) > time.Hour {
				_ = os.RemoveAll(filepath.Join(root, entry.Name()))
			}
			continue
		}
		if isPackageDirName(entry.Name()) {
			n, _ := strconv.ParseInt(entry.Name(), 10, 64)
			dirs = append(dirs, n)
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i] > dirs[j] })
	kept := 0
	for _, n := range dirs {
		name := strconv.FormatInt(n, 10)
		if name == current || kept < uiKeep-1 {
			if name != current {
				kept++
			}
			continue
		}
		_ = os.RemoveAll(filepath.Join(root, name))
	}
}

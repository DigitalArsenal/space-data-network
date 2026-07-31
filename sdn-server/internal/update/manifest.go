// Package update implements the signed SDN update lane for self-contained
// CLI bundles: manifest verification, staged payload management, and atomic
// bundle application with rollback. The manifest format and verification
// rules mirror desktop/src/sdn-updater/manifest.js and
// docs/sdn-signed-updater.md.
package update

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sigdomain"
)

const ManifestSchema = "org.spacedatanetwork.update.v1"

// TargetKindModuleUpdate is a target.kind value (G4) marking a manifest that
// delivers a targeted swap of one or more BUILT-IN module artifacts (e.g.
// flatsql) rather than a full bundle. It travels through the exact same
// signed-manifest + per-peer-encrypted carrier lane as a full-bundle update
// (see envelope.go / EncryptCarrierForRecipients); only Apply's install step
// differs (see ManifestModuleTarget and Manifest.IsModuleUpdate). A manifest
// also selects the targeted-swap path whenever Modules is non-empty, even if
// Target.Kind carries a different value — Kind and Modules are independent
// signals and either alone is sufficient.
const TargetKindModuleUpdate = "module-update"

type ManifestTarget struct {
	Platform string `json:"platform"`
	Arch     string `json:"arch"`
	Kind     string `json:"kind"`
}

// ManifestModuleTarget (G4) declares one built-in module artifact that a
// module-targeted update installs in place of a full bundle swap. Hash is
// the sha256 (lowercase hex) of the plaintext artifact bytes found at Path
// inside the decrypted/staged bundle payload; Path is the artifact's install
// location relative to the bundle root (e.g.
// "runtime/modules/flatsql/flatsql.wasm") and must stay inside the bundle
// root (validated with safeJoin both at manifest-validate time, structurally,
// and again at apply time against the real bundle root).
type ManifestModuleTarget struct {
	ID   string `json:"id"`
	Hash string `json:"hash"`
	Path string `json:"path"`
}

type ManifestBundle struct {
	Hash   string `json:"hash"`
	Size   int64  `json:"size"`
	Format string `json:"format"`
}

type ManifestWasm struct {
	Hash string `json:"hash"`
}

type ManifestSigning struct {
	KeyID     string `json:"key_id"`
	Algorithm string `json:"algorithm"`
	PublicKey string `json:"public_key,omitempty"`
	Signature string `json:"signature"`

	// StatementDomain selects WHICH preimage the signature covers. It is the
	// update lane's half of the domain-separation contract introduced for
	// module artifacts (internal/sigdomain, Seal Council 2026-07-30) and is
	// carried INSIDE the signed document — CanonicalManifestBytes removes only
	// signing.signature — so it is covered by the signature it describes and
	// cannot be added, removed or altered in flight.
	//
	// Absent  -> LEGACY form: ed25519 over the canonical manifest bytes.
	// Present -> it must be EXACTLY sigdomain.DomainUpdateManifestV1, and the
	//            signature covers sigdomain.Statement(domain, sha256(canonical)).
	//
	// There is no third case and no fallback: see assertSignature.
	StatementDomain string `json:"statement_domain,omitempty"`
}

type ManifestRollback struct {
	PreviousSequence *int64 `json:"previous_sequence,omitempty"`
	Reason           string `json:"reason,omitempty"`
}

// ManifestCompatibility carries host/runtime compatibility gates that must
// hold before an update is applied. MinKuboVersion is the G3 two-phase-apply
// gate: when set, the installed Kubo version (opts.InstalledKuboVersion at
// Validate time) must be >= this version or the manifest is rejected before
// any swap begins.
type ManifestCompatibility struct {
	MinKuboVersion string `json:"min_kubo_version,omitempty"`
}

type Manifest struct {
	Schema        string                 `json:"schema"`
	UpdateID      string                 `json:"update_id"`
	Version       string                 `json:"version"`
	Sequence      *int64                 `json:"sequence"`
	Channel       string                 `json:"channel"`
	CreatedAt     string                 `json:"created_at"`
	ExpiresAt     string                 `json:"expires_at"`
	Target        ManifestTarget         `json:"target"`
	Bundle        ManifestBundle         `json:"bundle"`
	Wasm          ManifestWasm           `json:"wasm"`
	Signing       ManifestSigning        `json:"signing"`
	Rollback      *ManifestRollback      `json:"rollback,omitempty"`
	Compatibility *ManifestCompatibility `json:"compatibility,omitempty"`
	// Modules (G4) is optional and additive: when non-empty it declares that
	// this update installs only the listed built-in module artifacts (a
	// targeted swap) instead of the full bundle. A manifest with no Modules
	// behaves exactly as before this field existed. Because
	// CanonicalManifestBytes canonicalizes the full generic JSON document
	// (not just the fields this struct models), adding this field is
	// signature-compatible: it is covered by the signature like any other
	// manifest field, old or new.
	Modules []ManifestModuleTarget `json:"modules,omitempty"`

	raw []byte
}

// IsModuleUpdate reports whether this manifest selects the G4
// module-targeted apply path: either Target.Kind is explicitly
// TargetKindModuleUpdate, or Modules is non-empty. Either signal alone is
// sufficient (see TargetKindModuleUpdate doc).
func (m *Manifest) IsModuleUpdate() bool {
	return m.Target.Kind == TargetKindModuleUpdate || len(m.Modules) > 0
}

// ParseManifest decodes a signed update manifest, retaining the raw bytes so
// the signature can be checked over the canonical form of the original
// document (including fields this struct does not model).
func ParseManifest(raw []byte) (*Manifest, error) {
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("parse update manifest: %w", err)
	}
	manifest.raw = append([]byte(nil), raw...)
	return &manifest, nil
}

// CanonicalManifestBytes reproduces the desktop updater's canonical JSON:
// every object key sorted lexically, signing.signature removed, compact
// separators, and no HTML escaping. Manifests are expected to be ASCII; the
// only known divergence from JSON.stringify is Go's escaping of U+2028 and
// U+2029, which must not appear in manifest values.
func CanonicalManifestBytes(raw []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var doc map[string]any
	if err := decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("parse update manifest: %w", err)
	}
	if signing, ok := doc["signing"].(map[string]any); ok {
		delete(signing, "signature")
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(doc); err != nil {
		return nil, fmt.Errorf("canonicalize update manifest: %w", err)
	}
	return bytes.TrimRight(out.Bytes(), "\n"), nil
}

var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func assertHash(value, message string) error {
	if !hashPattern.MatchString(value) {
		return errors.New(message)
	}
	return nil
}

func (m *Manifest) assertRequiredShape() error {
	if m.Schema != ManifestSchema {
		return errors.New("unsupported update manifest schema")
	}
	if m.UpdateID == "" {
		return errors.New("missing update id")
	}
	if m.Version == "" {
		return errors.New("missing update version")
	}
	if m.Sequence == nil {
		return errors.New("missing update sequence")
	}
	if m.Channel == "" {
		return errors.New("missing update channel")
	}
	if m.CreatedAt == "" {
		return errors.New("missing update creation time")
	}
	if m.ExpiresAt == "" {
		return errors.New("missing update expiration")
	}
	if m.Target.Platform == "" {
		return errors.New("missing update target platform")
	}
	if m.Target.Arch == "" {
		return errors.New("missing update target arch")
	}
	if m.Target.Kind == "" {
		return errors.New("missing update target kind")
	}
	if err := assertHash(m.Bundle.Hash, "missing update bundle hash"); err != nil {
		return err
	}
	if m.Bundle.Size < 0 {
		return errors.New("missing update bundle size")
	}
	if m.Bundle.Format == "" {
		return errors.New("missing update bundle format")
	}
	if err := assertHash(m.Wasm.Hash, "missing update wasm hash"); err != nil {
		return err
	}
	if m.Signing.KeyID == "" {
		return errors.New("missing update signing key id")
	}
	if m.Signing.Algorithm != "Ed25519" {
		return errors.New("unsupported update signing algorithm")
	}
	if m.Signing.Signature == "" {
		return errors.New("missing update signature")
	}
	if err := m.assertModuleTargets(); err != nil {
		return err
	}
	return nil
}

// assertModuleTargets validates the optional G4 Modules[] declaration: a
// module-update target kind requires at least one module target, and every
// module target needs a non-empty id, a valid 64-hex sha256 hash, and a path
// that stays inside the bundle root (checked structurally with safeJoin
// against a synthetic root — the real bundle root is only known at apply
// time, where the check is repeated against the real paths).
func (m *Manifest) assertModuleTargets() error {
	if m.Target.Kind == TargetKindModuleUpdate && len(m.Modules) == 0 {
		return errors.New("module-update target requires at least one module target")
	}
	seenPaths := make(map[string]bool, len(m.Modules))
	for i, module := range m.Modules {
		if strings.TrimSpace(module.ID) == "" {
			return fmt.Errorf("missing module target id at index %d", i)
		}
		if err := assertHash(module.Hash, fmt.Sprintf("missing or invalid module target hash for %s", module.ID)); err != nil {
			return err
		}
		if strings.TrimSpace(module.Path) == "" {
			return fmt.Errorf("missing module target path for %s", module.ID)
		}
		if _, err := safeJoin(moduleTargetValidationRoot, module.Path); err != nil {
			return fmt.Errorf("module target %s: %w", module.ID, err)
		}
		normalized := strings.ToLower(filepath.ToSlash(module.Path))
		if seenPaths[normalized] {
			return fmt.Errorf("duplicate module target path: %s", module.Path)
		}
		seenPaths[normalized] = true
	}
	return nil
}

// moduleTargetValidationRoot is a synthetic (never-created) base directory
// used only to run safeJoin's lexical "does this escape the root" check on a
// module target's Path at manifest-validate time, before any real bundle
// root exists on disk.
const moduleTargetValidationRoot = "bundle-root"

func platformMatches(manifestPlatform, hostPlatform string) bool {
	if manifestPlatform == hostPlatform {
		return true
	}
	// The signed-updater design uses Electron-style names; the CLI lane uses
	// Go runtime names. Accept the documented aliases in either direction.
	return (manifestPlatform == "win32" && hostPlatform == "windows") ||
		(manifestPlatform == "windows" && hostPlatform == "win32")
}

func archMatches(manifestArch, hostArch string) bool {
	if manifestArch == hostArch {
		return true
	}
	return (manifestArch == "x64" && hostArch == "amd64") ||
		(manifestArch == "amd64" && hostArch == "x64")
}

func (m *Manifest) assertTarget(platform, arch string) error {
	if !platformMatches(m.Target.Platform, platform) {
		return errors.New("update target platform mismatch")
	}
	if !archMatches(m.Target.Arch, arch) {
		return errors.New("update target arch mismatch")
	}
	return nil
}

func (m *Manifest) assertExpiration(now time.Time) error {
	expiresAt, err := time.Parse(time.RFC3339, m.ExpiresAt)
	if err != nil {
		return errors.New("invalid update expiration")
	}
	if !expiresAt.After(now) {
		return errors.New("update manifest expired")
	}
	return nil
}

// assertCompatibility enforces the G3 min_kubo_version gate. When the
// manifest declares no compatibility requirement, or the caller does not
// know the installed Kubo version, the check is skipped (back-compat: older
// callers that never set VerifyOptions.InstalledKuboVersion see no change
// in behavior). The gate only fires when both sides are known and the
// installed version is strictly older than required.
func (m *Manifest) assertCompatibility(installedKuboVersion string) error {
	if m.Compatibility == nil {
		return nil
	}
	minVersion := strings.TrimSpace(m.Compatibility.MinKuboVersion)
	if minVersion == "" {
		return nil
	}
	installed := strings.TrimSpace(installedKuboVersion)
	if installed == "" {
		return nil
	}
	cmp, err := compareVersions(installed, minVersion)
	if err != nil {
		return fmt.Errorf("invalid kubo version for compatibility check: %w", err)
	}
	if cmp < 0 {
		return fmt.Errorf("update requires kubo >= %s, installed %s", minVersion, installed)
	}
	return nil
}

// compareVersions compares two dot-separated numeric version strings
// (optionally "v"-prefixed, optionally carrying a "-"/"+" pre-release or
// build suffix which is ignored), returning -1, 0, or 1. Missing trailing
// segments are treated as 0 (e.g. "0.29" == "0.29.0").
func compareVersions(a, b string) (int, error) {
	pa, err := parseVersionParts(a)
	if err != nil {
		return 0, err
	}
	pb, err := parseVersionParts(b)
	if err != nil {
		return 0, err
	}
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			if x < y {
				return -1, nil
			}
			return 1, nil
		}
	}
	return 0, nil
}

func parseVersionParts(v string) ([]int, error) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	if idx := strings.IndexAny(v, "-+"); idx >= 0 {
		v = v[:idx]
	}
	if v == "" {
		return nil, fmt.Errorf("empty version string")
	}
	fields := strings.Split(v, ".")
	parts := make([]int, len(fields))
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil {
			return nil, fmt.Errorf("invalid version segment %q in %q", f, v)
		}
		parts[i] = n
	}
	return parts, nil
}

func (m *Manifest) assertSequence(currentSequence int64) error {
	if *m.Sequence > currentSequence {
		return nil
	}
	if m.Rollback == nil || m.Rollback.Reason == "" || m.Rollback.PreviousSequence == nil {
		return errors.New("update sequence rejected")
	}
	if *m.Sequence < *m.Rollback.PreviousSequence {
		return errors.New("update rollback sequence rejected")
	}
	return nil
}

// TrustedRoots maps signing key ids to base64-encoded Ed25519 public keys.
// Keys may be SPKI DER (as exported by the desktop tooling) or raw 32-byte
// public keys.
type TrustedRoots map[string]string

func decodeTrustedPublicKey(encoded string) (ed25519.PublicKey, error) {
	der, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		// Allow hex for raw keys such as the dev wallet signing pubkey.
		raw, hexErr := hex.DecodeString(encoded)
		if hexErr != nil || len(raw) != ed25519.PublicKeySize {
			return nil, errors.New("invalid trusted update root encoding")
		}
		return ed25519.PublicKey(raw), nil
	}
	if len(der) == ed25519.PublicKeySize {
		return ed25519.PublicKey(der), nil
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("parse trusted update root: %w", err)
	}
	key, ok := parsed.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("trusted update root is not an Ed25519 key")
	}
	return key, nil
}

func (m *Manifest) assertSignature(roots TrustedRoots) error {
	trusted, ok := roots[m.Signing.KeyID]
	if !ok || trusted == "" {
		return errors.New("untrusted update signing key")
	}
	if m.Signing.PublicKey != "" && m.Signing.PublicKey != trusted {
		return errors.New("update signing key mismatch")
	}
	publicKey, err := decodeTrustedPublicKey(trusted)
	if err != nil {
		return err
	}
	signature, err := base64.StdEncoding.DecodeString(m.Signing.Signature)
	if err != nil {
		return errors.New("invalid update signature encoding")
	}
	canonical, err := CanonicalManifestBytes(m.raw)
	if err != nil {
		return err
	}

	// TWO ACCEPTED FORMS, selected by a field that is itself signed. This
	// mirrors what internal/modulert already does for module artifacts, and it
	// is additive in the same way: a manifest with no statement_domain verifies
	// byte-for-byte as it did before this branch existed, so nothing previously
	// admitted is now refused.
	//
	// The asymmetry that makes it safe is that there is no DOWNGRADE path. A
	// manifest that names a domain is verified ONLY against that domain's
	// statement — never retried in legacy mode — so an attacker cannot strip
	// the field to change which preimage is checked: strip it and the canonical
	// bytes change, which invalidates the signature outright.
	//
	// And the domain is matched by EQUALITY against the one constant this lane
	// may use, deliberately NOT by sigdomain.Registered(). A registry lookup
	// would accept SDN-MODULE-PUBLICATION-V1 here, which is precisely the
	// cross-protocol replay the registry exists to prevent (Hermes CONCUR
	// condition, Seal Council 2026-07-31).
	preimage := canonical
	if domain := strings.TrimSpace(m.Signing.StatementDomain); domain != "" {
		if domain != sigdomain.DomainUpdateManifestV1 {
			return fmt.Errorf("unsupported update signature statement domain %q", domain)
		}
		sum := sha256.Sum256(canonical)
		statement, err := sigdomain.Statement(sigdomain.DomainUpdateManifestV1, sum[:])
		if err != nil {
			return fmt.Errorf("build update signature statement: %w", err)
		}
		preimage = statement
	}

	if !ed25519.Verify(publicKey, preimage, signature) {
		return errors.New("invalid update signature")
	}
	return nil
}

type VerifyOptions struct {
	Platform        string
	Arch            string
	CurrentSequence int64
	TrustedRoots    TrustedRoots
	Now             time.Time
	// InstalledKuboVersion is the currently-installed Kubo version, used to
	// enforce a manifest's compatibility.min_kubo_version gate (G3). Left
	// empty, the gate is skipped — existing callers built before this field
	// existed see no behavior change until a coordinator wires the actual
	// installed version in (see node.go TODO in the G3 report).
	InstalledKuboVersion string
}

type VerifyResult struct {
	UpdateID   string
	Version    string
	Sequence   int64
	Channel    string
	TargetKind string
	BundleHash string
	WasmHash   string
	BundleSize int64
}

// Validate checks the manifest shape, target, expiration, sequence policy,
// expected bundle hash, and signature, mirroring validateUpdateManifest in
// the desktop updater.
func (m *Manifest) Validate(bundleHash string, opts VerifyOptions) (*VerifyResult, error) {
	if err := m.assertRequiredShape(); err != nil {
		return nil, err
	}
	if err := m.assertTarget(opts.Platform, opts.Arch); err != nil {
		return nil, err
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	if err := m.assertExpiration(now); err != nil {
		return nil, err
	}
	if err := m.assertSequence(opts.CurrentSequence); err != nil {
		return nil, err
	}
	if err := m.assertCompatibility(opts.InstalledKuboVersion); err != nil {
		return nil, err
	}
	if m.Bundle.Hash != bundleHash {
		return nil, errors.New("update bundle hash mismatch")
	}
	if err := m.assertSignature(opts.TrustedRoots); err != nil {
		return nil, err
	}
	return &VerifyResult{
		UpdateID:   m.UpdateID,
		Version:    m.Version,
		Sequence:   *m.Sequence,
		Channel:    m.Channel,
		TargetKind: m.Target.Kind,
		BundleHash: m.Bundle.Hash,
		WasmHash:   m.Wasm.Hash,
		BundleSize: m.Bundle.Size,
	}, nil
}

// VerifyPayload validates the manifest against concrete carrier and bundle
// bytes, mirroring verifyDownloadedUpdatePayload in the desktop updater.
func (m *Manifest) VerifyPayload(wasmBytes, bundleBytes []byte, opts VerifyOptions) (*VerifyResult, error) {
	bundleHash := sha256Hex(bundleBytes)
	result, err := m.Validate(bundleHash, opts)
	if err != nil {
		return nil, err
	}
	if m.Wasm.Hash != sha256Hex(wasmBytes) {
		return nil, errors.New("update wasm hash mismatch")
	}
	if m.Bundle.Size != int64(len(bundleBytes)) {
		return nil, errors.New("update bundle size mismatch")
	}
	return result, nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

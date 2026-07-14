package api

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"

	"github.com/spacedatanetwork/sdn-server/internal/assetpin"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const (
	assetPinIntegrationFixtureBytes = 624
	assetPinIntegrationFixtureSHA   = "5a6a341ba01ccd7485a990ee8bfe636dea0e53dd3b6a313e31427f1042e9a285"
	assetPinIntegrationFixtureCID   = "bafkreic2ni2bxia4zv2ilkmq52f74y3n5ihfhxj3niyt4mkcp4ief2ncqu"

	assetPinIntegrationAudience         = "sdn-asset-models"
	assetPinIntegrationRepository       = "DigitalArsenal/asset-models"
	assetPinIntegrationRef              = "refs/heads/main"
	assetPinIntegrationPinWorkflow      = "DigitalArsenal/asset-models/.github/workflows/asset-loop.yml@refs/heads/main"
	assetPinIntegrationDecisionWorkflow = "DigitalArsenal/asset-models/.github/workflows/review-decision.yml@refs/heads/main"
	assetPinIntegrationActor            = "asset-integration-bot"
	assetPinIntegrationCommitSHA        = "0123456789abcdef0123456789abcdef01234567"
	assetPinIntegrationKeyID            = "asset-pin-integration-test-key"
)

// This is fixed, test-only RSA material. It is not a credential and cannot
// authorize anything outside the local httptest OIDC provider created below.
const assetPinIntegrationPrivateKeyPEM = `-----BEGIN PRIVATE KEY-----
MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQCqW33Wq6iXnXJe
FQWfx5HLAHPzGg6WtX68caQpmD9x9dXlLGQrxdbpkuQGTdkfr5b4Cf+7VR6EC7s4
NakaW3YgbOKJfMTHEHxdW/7w8p+BqDLHEDHyRmGr6/z7PoTvGz9REkJUm9zeVvob
x+AprPWFJxGZOY4BDECAHh5YULhwnGTbc3BTbsEzP8/qFb/zqtNDzMkQv9Y0Qrdx
rZT1Gj6aKQ18fqnkYFtL3YJ1RKp9LRSX62URrGDGQzu6tv9Q4Z265Zod2N29X+Pj
JFT83PPKbGqEH3AykYbjgF5nEcqab1g9buXJ9A7prygpO0PhdJaIPO16g8hD9HGY
sQdjr/HNAgMBAAECggEAJqXjApSnBt59V8LFJ96KwNc1du1uadp7Ch1t9NHJcv0m
rXtIrnWPsCXW/Wcj3wBi65q5HbLN3X8b1kC2QHiHcAvDyRU5P0AKNtPsHpWsgim6
e1a9Pg2hkvNSzVz9o5E26BmQWsmRbg+lZjAONuY6PR8D6xMXmD1DVM2AbODDNyil
DjGSyiD4/IaN+T+/MBQNktm4Qpy0kjmOsdMQg02HKkTb2AB6/F9Tc5aAWShG1rTZ
TL4GcY9ZrWfnvooU6ELmU87Ja8HSY/fNviwhkF454FCMdGt46JBlovmwFw8sxd4B
Ba/GTpKjojgE+bjyqHguvd7n3pol4GAWHbmgxnlQAQKBgQDanfBnBaibIYryOKv8
IZKIUJRFSWKtaoFoViOKTcqhhTndzfmZ9066qwos01sQbcHIkYK6KU+T7UpNAHqg
ldB3bkikzaKLRa5ap9XO3Dp7w3b87BWIU1hD6vaNV2IHjRM1EqTm5+qD2QMhSe7E
WWmpBn8tm4md69ciC+NtmZFbAQKBgQDHfPee+CtWh5/iuV9tLokCTucNuaRABEKi
3hMO4gY4vNh9oX9LWTpnNQGMmUrrR1lj094ttEOJeht8odLMMrPIT2rve/WdGERX
4n9b+ADa0NoShwdEBehd+tKhmFe+1T/rLc5ED0U3yH/TDMpkGH0h4jQcnduT4fbE
mjzbJeQSzQKBgCyZcAP0eZM8YpZLzXpgdv5sQfNop0LtqXzZpeJ/QEl3XnjLnpI0
i9E1N5wxejB907zRQrQr3Vo2XKQc5ud/6MmUrClC8lgrXQiNmObcsumw1MOAflwT
dLxWYPowy4Ty2OpI5W9d/M/tI+BUrutLumyLMMLjKk4XYQpHFpyzaZ4BAoGBALLO
Ck1M99tpWSApM6VzTo7pFiSxPs26g9fj4YU3hogYjJuew7BP3A9h7W+Ofx6AJ1lZ
MA4bQ2XYMwb1LTKmR4rF1H2vyCj09V0owSs4EdwP00dEDHkmKm8CQQVivVNpZQ9x
US6j2VD0v8316vrpEE/spvT3cTcOFNeHwABV6CYJAoGAVS21GokbTje1Cw2V4aQ4
rKajV8r5OO4gHkT3W6XCwzdaGbTi7oMKcTHORH8yZbNHatBFi/Xya10NsB3cBDA7
5rBIEVW9a8fnriCePR7tsSLl4YV/XBk1u1yZpZwqbVRg7rOImHhrt/8d2iE1N0O+
YoDSV/LF5AmZeM33QC94NKE=
-----END PRIVATE KEY-----`

func TestAssetPinMinimalGLBFixture(t *testing.T) {
	// The fixture is generated locally from the three positions and three
	// indices asserted below; it contains no downloaded model data.
	fixture, err := os.ReadFile("testdata/minimal.glb")
	if err != nil {
		t.Fatalf("read deterministic GLB fixture: %v", err)
	}
	if len(fixture) < 20 || string(fixture[:4]) != "glTF" {
		t.Fatalf("GLB header = %x, want glTF magic", fixture[:min(len(fixture), 4)])
	}
	if len(fixture) != assetPinIntegrationFixtureBytes {
		t.Fatalf("GLB byte length = %d, want locked fixture length %d", len(fixture), assetPinIntegrationFixtureBytes)
	}
	fixtureDigest := sha256.Sum256(fixture)
	if got := hex.EncodeToString(fixtureDigest[:]); got != assetPinIntegrationFixtureSHA {
		t.Fatalf("GLB SHA-256 = %q, want locked fixture digest %q", got, assetPinIntegrationFixtureSHA)
	}
	multihash, err := mh.Sum(fixture, mh.SHA2_256, -1)
	if err != nil {
		t.Fatalf("calculate fixture multihash: %v", err)
	}
	if got := cid.NewCidV1(cid.Raw, multihash).String(); got != assetPinIntegrationFixtureCID {
		t.Fatalf("GLB deterministic raw-leaf CID = %q, want %q", got, assetPinIntegrationFixtureCID)
	}
	if version := binary.LittleEndian.Uint32(fixture[4:8]); version != 2 {
		t.Fatalf("GLB version = %d, want 2", version)
	}
	if declared := binary.LittleEndian.Uint32(fixture[8:12]); declared != uint32(len(fixture)) {
		t.Fatalf("GLB declared length = %d, want %d", declared, len(fixture))
	}

	jsonLength := int(binary.LittleEndian.Uint32(fixture[12:16]))
	if jsonLength <= 0 || 20+jsonLength+8 > len(fixture) || binary.LittleEndian.Uint32(fixture[16:20]) != 0x4e4f534a {
		t.Fatal("GLB JSON chunk is missing or malformed")
	}
	var document struct {
		Asset struct {
			Version string `json:"version"`
		} `json:"asset"`
		Meshes []struct {
			Primitives []struct {
				Attributes map[string]int `json:"attributes"`
				Indices    int            `json:"indices"`
				Mode       int            `json:"mode"`
			} `json:"primitives"`
		} `json:"meshes"`
		Accessors []struct {
			ComponentType int    `json:"componentType"`
			Count         int    `json:"count"`
			Type          string `json:"type"`
		} `json:"accessors"`
	}
	if err := json.Unmarshal(fixture[20:20+jsonLength], &document); err != nil {
		t.Fatalf("decode GLB JSON chunk: %v", err)
	}
	if document.Asset.Version != "2.0" || len(document.Meshes) != 1 || len(document.Meshes[0].Primitives) != 1 {
		t.Fatalf("GLB scene shape = asset %q, %d meshes, %d primitives; want one glTF 2.0 triangle mesh", document.Asset.Version, len(document.Meshes), len(document.Meshes[0].Primitives))
	}
	primitive := document.Meshes[0].Primitives[0]
	if primitive.Mode != 4 || primitive.Attributes["POSITION"] != 0 || primitive.Indices != 1 {
		t.Fatalf("GLB primitive = %+v, want indexed TRIANGLES with POSITION accessor 0", primitive)
	}
	if len(document.Accessors) != 2 ||
		document.Accessors[0].ComponentType != 5126 || document.Accessors[0].Count != 3 || document.Accessors[0].Type != "VEC3" ||
		document.Accessors[1].ComponentType != 5123 || document.Accessors[1].Count != 3 || document.Accessors[1].Type != "SCALAR" {
		t.Fatalf("GLB accessors = %+v, want three float32 positions and three uint16 indices", document.Accessors)
	}

	binHeader := 20 + jsonLength
	binLength := int(binary.LittleEndian.Uint32(fixture[binHeader : binHeader+4]))
	if binary.LittleEndian.Uint32(fixture[binHeader+4:binHeader+8]) != 0x004e4942 || binHeader+8+binLength != len(fixture) {
		t.Fatal("GLB BIN chunk is missing or malformed")
	}
	bin := fixture[binHeader+8:]
	if binLength != 44 ||
		binary.LittleEndian.Uint32(bin[12:16]) != 0x3f800000 ||
		binary.LittleEndian.Uint32(bin[28:32]) != 0x3f800000 ||
		binary.LittleEndian.Uint16(bin[36:38]) != 0 ||
		binary.LittleEndian.Uint16(bin[38:40]) != 1 ||
		binary.LittleEndian.Uint16(bin[40:42]) != 2 {
		t.Fatalf("GLB BIN chunk does not encode the expected one-triangle geometry")
	}
}

func TestAssetPinLifecycleEndToEnd(t *testing.T) {
	fixture, err := os.ReadFile("testdata/minimal.glb")
	if err != nil {
		t.Fatalf("read deterministic GLB fixture: %v", err)
	}
	now := time.Date(2020, time.July, 13, 16, 30, 45, 123456789, time.UTC)
	harness := newAssetPinIntegrationHarness(t, fixture, now)
	ctx := context.Background()

	const (
		approvedCandidate = "integration-approved-vehicle"
		expiringCandidate = "integration-expiring-vehicle"
		issueNumber       = int64(4242)
	)
	decisionBytes := sha256.Sum256([]byte("approve integration-approved-vehicle at issue 4242"))
	decisionSHA := hex.EncodeToString(decisionBytes[:])

	uploadToken := harness.oidc.signToken(t, assetPinIntegrationPinWorkflow, "1001")
	uploadResponse := harness.postUpload(t, approvedCandidate, uploadToken)
	firstResult := decodeAssetPinIntegrationUpload(t, uploadResponse, http.StatusCreated)
	if firstResult.CID != assetPinIntegrationFixtureCID || firstResult.SHA256 != assetPinIntegrationFixtureSHA ||
		firstResult.ByteLength != assetPinIntegrationFixtureBytes || firstResult.PinState != string(storage.AssetReferenceStaged) ||
		firstResult.AlreadyExisted || firstResult.GatewayURL != "https://gateway.example.test/ipfs/"+assetPinIntegrationFixtureCID {
		t.Fatalf("first upload response = %+v, want exact newly-pinned fixture identity", firstResult)
	}
	firstReference := requireAssetPinIntegrationReference(t, ctx, harness.store, approvedCandidate)
	if firstReference.ReferenceKey != stableAssetPinID("asset-pin-reference:v1\n"+approvedCandidate) ||
		firstReference.CID != assetPinIntegrationFixtureCID || firstReference.SHA256 != assetPinIntegrationFixtureSHA ||
		firstReference.ByteCount != assetPinIntegrationFixtureBytes || firstReference.State != storage.AssetReferenceStaged ||
		firstReference.SourceURL != "https://example.test/generated/minimal.glb" || firstReference.LicenseName != "CC0-1.0" ||
		firstReference.Attribution != "Generated locally; no external asset source" ||
		firstReference.MetadataJSON != string(assetPinIntegrationMetadata(approvedCandidate)) ||
		firstReference.WorkflowRunID != "1001" || !firstReference.CreatedAt.Equal(now) || !firstReference.UpdatedAt.Equal(now) ||
		!firstReference.ExpiresAt.Equal(now.Add(90*24*time.Hour)) {
		t.Fatalf("first durable reference = %+v, want exact staged fixture reference", firstReference)
	}
	firstUploadAudit := requireAssetPinIntegrationAuditEvent(t, ctx, harness.store, approvedCandidate, "asset_pin_upload", "pinned")
	if firstUploadAudit.TokenDigest != assetPinIntegrationTokenDigest(uploadToken) {
		t.Fatalf("first upload token digest = %q, want digest of accepted token", firstUploadAudit.TokenDigest)
	}
	requireAssetPinIntegrationReceipt(t, ctx, harness.store, uploadToken, "1001", now)
	harness.kubo.requireCalls(t, []assetPinIntegrationKuboCall{
		{Path: "/api/v0/add", RawQuery: assetPinIntegrationOnlyHashQuery(), SHA256: assetPinIntegrationFixtureSHA, ByteCount: assetPinIntegrationFixtureBytes},
		{Path: "/api/v0/add", RawQuery: assetPinIntegrationPinQuery(), SHA256: assetPinIntegrationFixtureSHA, ByteCount: assetPinIntegrationFixtureBytes},
	})

	harness.reopenStore(t)
	replayedReference := requireAssetPinIntegrationReference(t, ctx, harness.store, approvedCandidate)
	if replayedReference != firstReference {
		t.Fatalf("replayed reference = %+v, want pre-close reference %+v", replayedReference, firstReference)
	}
	replayedAudit := requireAssetPinIntegrationAuditEvent(t, ctx, harness.store, approvedCandidate, "asset_pin_upload", "pinned")
	if replayedAudit.EventID != firstUploadAudit.EventID || replayedAudit.MutationDigest != firstUploadAudit.MutationDigest {
		t.Fatalf("replayed upload audit = %+v, want event %q mutation %q", replayedAudit, firstUploadAudit.EventID, firstUploadAudit.MutationDigest)
	}
	requireAssetPinIntegrationReceipt(t, ctx, harness.store, uploadToken, "1001", now)

	dedupToken := harness.oidc.signToken(t, assetPinIntegrationPinWorkflow, "1002")
	dedupResponse := harness.postUpload(t, expiringCandidate, dedupToken)
	dedupResult := decodeAssetPinIntegrationUpload(t, dedupResponse, http.StatusCreated)
	if dedupResult.CID != assetPinIntegrationFixtureCID || dedupResult.SHA256 != assetPinIntegrationFixtureSHA ||
		dedupResult.ByteLength != assetPinIntegrationFixtureBytes || !dedupResult.AlreadyExisted {
		t.Fatalf("second upload response = %+v, want exact shared-CID deduplication", dedupResult)
	}
	secondReference := requireAssetPinIntegrationReference(t, ctx, harness.store, expiringCandidate)
	if secondReference.ReferenceKey == firstReference.ReferenceKey || secondReference.CID != firstReference.CID ||
		secondReference.SHA256 != firstReference.SHA256 || secondReference.State != storage.AssetReferenceStaged {
		t.Fatalf("second durable reference = %+v, want distinct staged owner of shared content", secondReference)
	}
	dedupAudit := requireAssetPinIntegrationAuditEvent(t, ctx, harness.store, expiringCandidate, "asset_pin_upload", "deduplicated")
	if dedupAudit.TokenDigest != assetPinIntegrationTokenDigest(dedupToken) {
		t.Fatalf("dedup audit token digest = %q, want accepted token digest", dedupAudit.TokenDigest)
	}
	requireAssetPinIntegrationReceipt(t, ctx, harness.store, dedupToken, "1002", now)

	reviewedAt := now.Add(time.Hour)
	reviewToken := harness.oidc.signToken(t, assetPinIntegrationPinWorkflow, "1003")
	reviewResponse := harness.postReferenceState(t, reviewToken, assetReferenceStateRequest{
		CandidateKey: approvedCandidate,
		DecidedAt:    reviewedAt.Format(time.RFC3339Nano),
		IssueNumber:  issueNumber,
		State:        string(storage.AssetReferenceReviewOpen),
	})
	reviewResult := decodeAssetPinIntegrationState(t, reviewResponse, http.StatusOK)
	if reviewResult.State != storage.AssetReferenceReviewOpen || reviewResult.ExpiresAt == nil || !reviewResult.ExpiresAt.Equal(firstReference.ExpiresAt) {
		t.Fatalf("review-open response = %+v, want original staged expiry", reviewResult)
	}

	approvedAt := now.Add(2 * time.Hour)
	decisionToken := harness.oidc.signToken(t, assetPinIntegrationDecisionWorkflow, "1004")
	decisionResponse := harness.postReferenceState(t, decisionToken, assetReferenceStateRequest{
		CandidateKey:   approvedCandidate,
		DecidedAt:      approvedAt.Format(time.RFC3339Nano),
		DecisionSHA256: decisionSHA,
		IssueNumber:    issueNumber,
		State:          string(storage.AssetReferenceApproved),
	})
	decisionResult := decodeAssetPinIntegrationState(t, decisionResponse, http.StatusOK)
	if decisionResult.State != storage.AssetReferenceApproved || decisionResult.ExpiresAt != nil {
		t.Fatalf("approval response = %+v, want permanent approved reference", decisionResult)
	}
	approvedReference := requireAssetPinIntegrationReference(t, ctx, harness.store, approvedCandidate)
	if approvedReference.State != storage.AssetReferenceApproved || approvedReference.GitHubIssue != issueNumber ||
		approvedReference.DecisionSHA256 != decisionSHA || !approvedReference.ExpiresAt.IsZero() {
		t.Fatalf("approved durable reference = %+v, want permanent issue-bound approval", approvedReference)
	}
	reviewAudit := requireAssetPinIntegrationAuditEvent(t, ctx, harness.store, approvedCandidate, "asset_reference_state", "review_open")
	if reviewAudit.TokenDigest != assetPinIntegrationTokenDigest(reviewToken) {
		t.Fatalf("review audit token digest = %q, want accepted token digest", reviewAudit.TokenDigest)
	}
	approvalAudit := requireAssetPinIntegrationAuditEvent(t, ctx, harness.store, approvedCandidate, "asset_reference_state", "approved")
	if approvalAudit.TokenDigest != assetPinIntegrationTokenDigest(decisionToken) {
		t.Fatalf("approval audit token digest = %q, want accepted token digest", approvalAudit.TokenDigest)
	}
	requireAssetPinIntegrationReceipt(t, ctx, harness.store, reviewToken, "1003", now)
	requireAssetPinIntegrationReceipt(t, ctx, harness.store, decisionToken, "1004", now)

	recovery, err := assetpin.NewFileAssetPinRecoveryStore(harness.dataDir)
	if err != nil {
		t.Fatalf("open production recovery store for retention: %v", err)
	}
	retentionPins, err := assetpin.NewKuboRetentionClient(harness.kubo.server.URL)
	if err != nil {
		t.Fatalf("construct production Kubo retention client: %v", err)
	}
	retainer, err := assetpin.NewRetainer(assetpin.RetainerOptions{
		Store:        harness.store,
		Pins:         retentionPins,
		Recovery:     recovery,
		Gate:         harness.gate,
		CallTimeout:  time.Second,
		SweepTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("construct production asset retainer: %v", err)
	}
	if err := retainer.Sweep(ctx, now.Add(91*24*time.Hour)); err != nil {
		t.Fatalf("sweep expired shared-CID reference: %v", err)
	}
	if _, found, err := harness.store.FindAssetPinReferenceByCandidateKey(ctx, expiringCandidate); err != nil || found {
		t.Fatalf("expired candidate after retention = found %v, error %v; want deleted", found, err)
	}
	retainedReference := requireAssetPinIntegrationReference(t, ctx, harness.store, approvedCandidate)
	if retainedReference.State != storage.AssetReferenceApproved || !retainedReference.ExpiresAt.IsZero() {
		t.Fatalf("approved shared-CID owner after sweep = %+v, want permanent approval", retainedReference)
	}
	stillPinned, err := retentionPins.IsAssetCIDPinned(ctx, assetPinIntegrationFixtureCID)
	if err != nil || !stillPinned || !harness.kubo.isPinned() {
		t.Fatalf("shared approved CID recursive pin = %v, %v (fake state %v); want retained", stillPinned, err, harness.kubo.isPinned())
	}

	allEvents, err := harness.store.ListAssetPinAuditEvents(ctx, storage.AssetPinAuditEventQuery{})
	if err != nil {
		t.Fatalf("list complete lifecycle audit: %v", err)
	}
	requireAssetPinIntegrationLifecycleAudit(t, allEvents, approvedCandidate, expiringCandidate, decisionSHA)
	harness.kubo.requireLifecycleEvidence(t)
	harness.oidc.requireEvidence(t)

	rawTokens := []string{uploadToken, dedupToken, reviewToken, decisionToken}
	for _, event := range allEvents {
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("marshal lifecycle audit event: %v", err)
		}
		assertAssetPinIntegrationNoRawTokens(t, encoded, rawTokens)
	}
	for _, responseBody := range harness.responseBodies {
		assertAssetPinIntegrationNoRawTokens(t, responseBody, rawTokens)
	}
	harness.closeStore(t)
	assertAssetPinIntegrationTreeHasNoRawTokens(t, harness.root, rawTokens)
}

func assetPinIntegrationOnlyHashQuery() string {
	return url.Values{
		"chunker":             {"size-262144"},
		"cid-version":         {"1"},
		"hash":                {"sha2-256"},
		"only-hash":           {"true"},
		"pin":                 {"false"},
		"progress":            {"false"},
		"raw-leaves":          {"true"},
		"wrap-with-directory": {"false"},
	}.Encode()
}

func assetPinIntegrationPinQuery() string {
	return url.Values{
		"chunker":             {"size-262144"},
		"cid-version":         {"1"},
		"hash":                {"sha2-256"},
		"pin":                 {"true"},
		"progress":            {"false"},
		"raw-leaves":          {"true"},
		"wrap-with-directory": {"false"},
	}.Encode()
}

func assetPinIntegrationTokenDigest(rawToken string) string {
	digest := sha256.Sum256([]byte(rawToken))
	return hex.EncodeToString(digest[:])
}

type assetPinIntegrationHarness struct {
	root           string
	dataDir        string
	kuboRepo       string
	storePath      string
	fixture        []byte
	now            time.Time
	oidc           *assetPinIntegrationOIDC
	kubo           *assetPinIntegrationKubo
	config         config.AssetPinConfig
	gate           *assetpin.MutationGate
	pinner         *KuboAssetPinner
	store          *storage.FlatSQLStore
	handler        *AssetPinHandler
	mux            *http.ServeMux
	responseBodies [][]byte
}

func newAssetPinIntegrationHarness(t *testing.T, fixture []byte, now time.Time) *assetPinIntegrationHarness {
	t.Helper()
	root := t.TempDir()
	harness := &assetPinIntegrationHarness{
		root:      root,
		dataDir:   filepath.Join(root, "asset-data"),
		kuboRepo:  filepath.Join(root, "kubo-repo"),
		storePath: filepath.Join(root, "flatsql"),
		fixture:   append([]byte(nil), fixture...),
		now:       now,
		oidc:      newAssetPinIntegrationOIDC(t),
		kubo:      newAssetPinIntegrationKubo(t, fixture),
		gate:      assetpin.NewMutationGate(),
	}
	for _, directory := range []string{harness.dataDir, harness.kuboRepo} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatalf("create integration directory %s: %v", directory, err)
		}
	}
	harness.config = config.AssetPinConfig{
		Enabled:          true,
		Issuer:           harness.oidc.server.URL,
		Audience:         assetPinIntegrationAudience,
		Repository:       assetPinIntegrationRepository,
		Ref:              assetPinIntegrationRef,
		PinWorkflow:      assetPinIntegrationPinWorkflow,
		DecisionWorkflow: assetPinIntegrationDecisionWorkflow,
		GatewayURL:       "https://gateway.example.test/ipfs",
		KuboRepoPath:     harness.kuboRepo,
		MaxUploadBytes:   1 << 20,
		MinFreeBytes:     1,
	}
	var err error
	harness.pinner, err = NewKuboAssetPinner(harness.kubo.server.URL)
	if err != nil {
		t.Fatalf("construct production Kubo asset pinner: %v", err)
	}
	harness.store = openAssetPinIntegrationStore(t, harness.storePath)
	harness.bindHandler(t)
	t.Cleanup(func() {
		if harness.store != nil {
			_ = harness.store.Close()
			harness.store = nil
		}
	})
	return harness
}

func openAssetPinIntegrationStore(t *testing.T, path string) *storage.FlatSQLStore {
	t.Helper()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("construct FlatSQL validator: %v", err)
	}
	store, err := storage.NewFlatSQLStore(path, validator, storage.WithDeferredBootRebuilds())
	if err != nil {
		t.Fatalf("open real temporary FlatSQL store: %v", err)
	}
	return store
}

func (h *assetPinIntegrationHarness) bindHandler(t *testing.T) {
	t.Helper()
	consumer := func(ctx context.Context, digest string, expiresAt time.Time, claims assetpin.Claims) error {
		err := h.store.ConsumeAssetOIDCToken(ctx, storage.AssetOIDCReceipt{
			Digest:      digest,
			ExpiresAt:   expiresAt.UTC(),
			Repository:  claims.Repository,
			Ref:         claims.Ref,
			WorkflowRef: claims.WorkflowRef,
			Actor:       claims.Actor,
			RunID:       claims.RunID,
			RunAttempt:  claims.RunAttempt,
			SHA:         claims.SHA,
			ConsumedAt:  h.now,
		})
		if errors.Is(err, storage.ErrAssetOIDCTokenReplay) {
			return assetpin.ErrTokenReplay
		}
		return err
	}
	verifier, err := assetpin.NewVerifier(context.Background(), h.config, consumer)
	if err != nil {
		t.Fatalf("construct production OIDC verifier: %v", err)
	}
	h.handler, err = NewAssetPinHandler(AssetPinHandlerOptions{
		Verifier: verifier,
		Store:    h.store,
		Capacity: assetPinIntegrationCapacity(1 << 40),
		Pinner:   h.pinner,
		Gate:     h.gate,
		Config:   h.config,
		DataDir:  h.dataDir,
		Clock:    func() time.Time { return h.now },
	})
	if err != nil {
		t.Fatalf("construct real asset pin handler: %v", err)
	}
	h.mux = http.NewServeMux()
	h.handler.RegisterRoutes(h.mux)
}

func (h *assetPinIntegrationHarness) reopenStore(t *testing.T) {
	t.Helper()
	if h.store == nil {
		t.Fatal("cannot reopen a nil FlatSQL store")
	}
	if err := h.store.Close(); err != nil {
		t.Fatalf("close FlatSQL store before ledger replay: %v", err)
	}
	h.store = nil
	h.store = openAssetPinIntegrationStore(t, h.storePath)
	h.bindHandler(t)
}

func (h *assetPinIntegrationHarness) closeStore(t *testing.T) {
	t.Helper()
	if h.store == nil {
		return
	}
	if err := h.store.Close(); err != nil {
		t.Fatalf("close final FlatSQL store: %v", err)
	}
	h.store = nil
}

func (h *assetPinIntegrationHarness) postUpload(t *testing.T, candidateKey, token string) *httptest.ResponseRecorder {
	t.Helper()
	metadata := assetPinIntegrationMetadata(candidateKey)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	metadataPart, err := writer.CreateFormField("metadata")
	if err != nil {
		t.Fatalf("create upload metadata part: %v", err)
	}
	if _, err := metadataPart.Write(metadata); err != nil {
		t.Fatalf("write upload metadata part: %v", err)
	}
	filePart, err := writer.CreateFormFile("file", "minimal.glb")
	if err != nil {
		t.Fatalf("create upload fixture part: %v", err)
	}
	if _, err := filePart.Write(h.fixture); err != nil {
		t.Fatalf("write upload fixture part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close upload multipart body: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/assets/pin", &body)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	h.mux.ServeHTTP(response, request)
	h.responseBodies = append(h.responseBodies, append([]byte(nil), response.Body.Bytes()...))
	return response
}

func assetPinIntegrationMetadata(candidateKey string) []byte {
	return []byte(fmt.Sprintf(
		`{"attribution":"Generated locally; no external asset source","candidateKey":%q,"licenseName":"CC0-1.0","schemaVersion":1,"sha256":"%s","sourceUrl":"https://example.test/generated/minimal.glb"}`,
		candidateKey,
		assetPinIntegrationFixtureSHA,
	))
}

func (h *assetPinIntegrationHarness) postReferenceState(t *testing.T, token string, requestBody assetReferenceStateRequest) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(requestBody)
	if err != nil {
		t.Fatalf("marshal canonical reference-state request: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/assets/reference-state", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	h.mux.ServeHTTP(response, request)
	h.responseBodies = append(h.responseBodies, append([]byte(nil), response.Body.Bytes()...))
	return response
}

type assetPinIntegrationCapacity uint64

func (capacity assetPinIntegrationCapacity) AvailableBytes(string) (uint64, error) {
	return uint64(capacity), nil
}

func decodeAssetPinIntegrationUpload(t *testing.T, response *httptest.ResponseRecorder, wantStatus int) assetPinResponse {
	t.Helper()
	if response.Code != wantStatus {
		t.Fatalf("asset upload status = %d, want %d; body=%s", response.Code, wantStatus, response.Body.String())
	}
	if response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("asset upload Content-Type = %q, want application/json", response.Header().Get("Content-Type"))
	}
	var decoded assetPinResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode asset upload response: %v", err)
	}
	return decoded
}

func decodeAssetPinIntegrationState(t *testing.T, response *httptest.ResponseRecorder, wantStatus int) assetReferenceStateResponse {
	t.Helper()
	if response.Code != wantStatus {
		t.Fatalf("reference-state status = %d, want %d; body=%s", response.Code, wantStatus, response.Body.String())
	}
	if response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("reference-state Content-Type = %q, want application/json", response.Header().Get("Content-Type"))
	}
	var decoded assetReferenceStateResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode reference-state response: %v", err)
	}
	return decoded
}

type assetPinIntegrationOIDC struct {
	server     *httptest.Server
	privateKey *rsa.PrivateKey
	mu         sync.Mutex
	discovery  int
	jwks       int
}

func newAssetPinIntegrationOIDC(t *testing.T) *assetPinIntegrationOIDC {
	t.Helper()
	block, _ := pem.Decode([]byte(assetPinIntegrationPrivateKeyPEM))
	if block == nil {
		t.Fatal("decode fixed test-only OIDC private key PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse fixed test-only OIDC private key: %v", err)
	}
	privateKey, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		t.Fatalf("fixed test-only OIDC key type = %T, want RSA", parsed)
	}
	if err := privateKey.Validate(); err != nil {
		t.Fatalf("validate fixed test-only OIDC key: %v", err)
	}
	provider := &assetPinIntegrationOIDC{privateKey: privateKey}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		provider.mu.Lock()
		provider.discovery++
		provider.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                provider.server.URL,
			"authorization_endpoint":                provider.server.URL + "/authorize",
			"token_endpoint":                        provider.server.URL + "/token",
			"jwks_uri":                              provider.server.URL + "/keys",
			"response_types_supported":              []string{"id_token"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		provider.mu.Lock()
		provider.jwks++
		provider.mu.Unlock()
		publicKey := provider.privateKey.PublicKey
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{{
				"kty": "RSA",
				"use": "sig",
				"kid": assetPinIntegrationKeyID,
				"alg": "RS256",
				"n":   base64.RawURLEncoding.EncodeToString(publicKey.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(publicKey.E)).Bytes()),
			}},
		})
	})
	provider.server = httptest.NewServer(mux)
	t.Cleanup(provider.server.Close)
	return provider
}

func (p *assetPinIntegrationOIDC) signToken(t *testing.T, workflow, runID string) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{
		"alg": "RS256",
		"kid": assetPinIntegrationKeyID,
		"typ": "JWT",
	})
	if err != nil {
		t.Fatalf("marshal integration token header: %v", err)
	}
	claims, err := json.Marshal(map[string]any{
		"actor":        assetPinIntegrationActor,
		"aud":          assetPinIntegrationAudience,
		"exp":          time.Date(2099, time.January, 1, 0, 0, 0, 0, time.UTC).Unix(),
		"iat":          time.Date(2019, time.January, 1, 0, 0, 0, 0, time.UTC).Unix(),
		"iss":          p.server.URL,
		"jti":          "asset-pin-integration-" + runID,
		"nbf":          time.Date(2019, time.January, 1, 0, 0, 0, 0, time.UTC).Unix(),
		"ref":          assetPinIntegrationRef,
		"repository":   assetPinIntegrationRepository,
		"run_attempt":  "1",
		"run_id":       runID,
		"sha":          assetPinIntegrationCommitSHA,
		"sub":          "repo:" + assetPinIntegrationRepository + ":ref:" + assetPinIntegrationRef,
		"workflow_ref": workflow,
	})
	if err != nil {
		t.Fatalf("marshal integration token claims: %v", err)
	}
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(input))
	signature, err := rsa.SignPKCS1v15(nil, p.privateKey, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign integration OIDC token: %v", err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func (p *assetPinIntegrationOIDC) requireEvidence(t *testing.T) {
	t.Helper()
	p.mu.Lock()
	discovery, jwks := p.discovery, p.jwks
	p.mu.Unlock()
	if discovery < 2 || jwks < 2 {
		t.Fatalf("local OIDC evidence = %d discovery and %d JWKS requests, want at least two of each across reopen", discovery, jwks)
	}
}

type assetPinIntegrationKuboCall struct {
	Path      string
	RawQuery  string
	SHA256    string
	ByteCount int64
}

type assetPinIntegrationKubo struct {
	server  *httptest.Server
	fixture []byte
	mu      sync.Mutex
	pinned  bool
	calls   []assetPinIntegrationKuboCall
}

func newAssetPinIntegrationKubo(t *testing.T, fixture []byte) *assetPinIntegrationKubo {
	t.Helper()
	kubo := &assetPinIntegrationKubo{fixture: append([]byte(nil), fixture...)}
	kubo.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kubo.serveHTTP(t, w, r)
	}))
	t.Cleanup(kubo.server.Close)
	return kubo
}

func (k *assetPinIntegrationKubo) serveHTTP(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	if r.Method != http.MethodPost {
		k.fail(t, w, "Kubo method = %q, want POST", r.Method)
		return
	}
	switch r.URL.Path {
	case "/api/v0/add":
		k.serveAdd(t, w, r)
	case "/api/v0/pin/ls":
		wantQuery := url.Values{"arg": {assetPinIntegrationFixtureCID}, "type": {"recursive"}}.Encode()
		if r.URL.RawQuery != wantQuery {
			k.fail(t, w, "Kubo pin/ls query = %q, want %q", r.URL.RawQuery, wantQuery)
			return
		}
		k.mu.Lock()
		k.calls = append(k.calls, assetPinIntegrationKuboCall{Path: r.URL.Path, RawQuery: r.URL.RawQuery})
		pinned := k.pinned
		k.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if !pinned {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprintf(w, `{"Message":"path %s is not pinned","Code":0,"Type":"error"}`, assetPinIntegrationFixtureCID)
			return
		}
		_, _ = fmt.Fprintf(w, `{"Keys":{"%s":{"Type":"recursive"}}}`, assetPinIntegrationFixtureCID)
	case "/api/v0/pin/rm":
		wantQuery := url.Values{"arg": {assetPinIntegrationFixtureCID}}.Encode()
		if r.URL.RawQuery != wantQuery {
			k.fail(t, w, "Kubo pin/rm query = %q, want %q", r.URL.RawQuery, wantQuery)
			return
		}
		k.mu.Lock()
		k.calls = append(k.calls, assetPinIntegrationKuboCall{Path: r.URL.Path, RawQuery: r.URL.RawQuery})
		wasPinned := k.pinned
		k.pinned = false
		k.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if !wasPinned {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprintf(w, `{"Message":"path %s is not pinned","Code":0,"Type":"error"}`, assetPinIntegrationFixtureCID)
			return
		}
		_, _ = fmt.Fprintf(w, `{"Pins":["%s"]}`, assetPinIntegrationFixtureCID)
	default:
		k.fail(t, w, "unexpected Kubo path %q", r.URL.Path)
	}
}

func (k *assetPinIntegrationKubo) serveAdd(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	query := r.URL.Query()
	onlyHash := query.Get("only-hash") == "true"
	wantQuery := assetPinIntegrationPinQuery()
	if onlyHash {
		wantQuery = assetPinIntegrationOnlyHashQuery()
	}
	if r.URL.RawQuery != wantQuery {
		k.fail(t, w, "Kubo add query = %q, want %q", r.URL.RawQuery, wantQuery)
		return
	}
	reader, err := r.MultipartReader()
	if err != nil {
		k.fail(t, w, "parse Kubo add multipart body: %v", err)
		return
	}
	part, err := reader.NextPart()
	if err != nil {
		k.fail(t, w, "read Kubo add file part: %v", err)
		return
	}
	if part.FormName() != "file" || strings.TrimSpace(part.FileName()) == "" {
		k.fail(t, w, "Kubo add multipart identity = %q/%q, want named file part", part.FormName(), part.FileName())
		return
	}
	payload, err := io.ReadAll(io.LimitReader(part, int64(len(k.fixture))+1))
	_ = part.Close()
	if err != nil {
		k.fail(t, w, "read Kubo add fixture: %v", err)
		return
	}
	if trailing, err := reader.NextPart(); !errors.Is(err, io.EOF) {
		if trailing != nil {
			_ = trailing.Close()
		}
		k.fail(t, w, "Kubo add contained a trailing multipart part: %v", err)
		return
	}
	if !bytes.Equal(payload, k.fixture) {
		k.fail(t, w, "Kubo add received %d bytes that differ from the locked fixture", len(payload))
		return
	}
	digest := sha256.Sum256(payload)
	call := assetPinIntegrationKuboCall{
		Path:      r.URL.Path,
		RawQuery:  r.URL.RawQuery,
		SHA256:    hex.EncodeToString(digest[:]),
		ByteCount: int64(len(payload)),
	}
	k.mu.Lock()
	k.calls = append(k.calls, call)
	if !onlyHash {
		k.pinned = true
	}
	k.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"Name":"minimal.glb","Hash":"%s","Size":"%s"}`, assetPinIntegrationFixtureCID, strconv.Itoa(len(payload)))
}

func (k *assetPinIntegrationKubo) fail(t *testing.T, w http.ResponseWriter, format string, values ...any) {
	t.Helper()
	t.Errorf(format, values...)
	http.Error(w, "invalid local Kubo request", http.StatusBadRequest)
}

func (k *assetPinIntegrationKubo) snapshotCalls() []assetPinIntegrationKuboCall {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]assetPinIntegrationKuboCall(nil), k.calls...)
}

func (k *assetPinIntegrationKubo) requireCalls(t *testing.T, want []assetPinIntegrationKuboCall) {
	t.Helper()
	got := k.snapshotCalls()
	if len(got) != len(want) {
		t.Fatalf("Kubo calls = %+v, want %+v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("Kubo call %d = %+v, want %+v", index, got[index], want[index])
		}
	}
}

func (k *assetPinIntegrationKubo) requireLifecycleEvidence(t *testing.T) {
	t.Helper()
	pinLookupQuery := url.Values{"arg": {assetPinIntegrationFixtureCID}, "type": {"recursive"}}.Encode()
	k.requireCalls(t, []assetPinIntegrationKuboCall{
		{Path: "/api/v0/add", RawQuery: assetPinIntegrationOnlyHashQuery(), SHA256: assetPinIntegrationFixtureSHA, ByteCount: assetPinIntegrationFixtureBytes},
		{Path: "/api/v0/add", RawQuery: assetPinIntegrationPinQuery(), SHA256: assetPinIntegrationFixtureSHA, ByteCount: assetPinIntegrationFixtureBytes},
		{Path: "/api/v0/pin/ls", RawQuery: pinLookupQuery},
		{Path: "/api/v0/pin/ls", RawQuery: pinLookupQuery},
		{Path: "/api/v0/pin/ls", RawQuery: pinLookupQuery},
	})
}

func (k *assetPinIntegrationKubo) isPinned() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.pinned
}

func requireAssetPinIntegrationReference(t *testing.T, ctx context.Context, store *storage.FlatSQLStore, candidateKey string) storage.AssetPinReference {
	t.Helper()
	reference, found, err := store.FindAssetPinReferenceByCandidateKey(ctx, candidateKey)
	if err != nil || !found {
		t.Fatalf("find asset reference for %q = %+v, %v, %v; want present", candidateKey, reference, found, err)
	}
	return reference
}

func requireAssetPinIntegrationAuditEvent(t *testing.T, ctx context.Context, store *storage.FlatSQLStore, candidateKey, kind, result string) storage.AssetPinAuditEvent {
	t.Helper()
	events, err := store.ListAssetPinAuditEvents(ctx, storage.AssetPinAuditEventQuery{CandidateKey: candidateKey, Kind: kind})
	if err != nil {
		t.Fatalf("list %s audit for %q: %v", kind, candidateKey, err)
	}
	matching := make([]storage.AssetPinAuditEvent, 0, 1)
	for _, event := range events {
		if event.Result == result {
			matching = append(matching, event)
		}
	}
	if len(matching) != 1 {
		t.Fatalf("%s/%s audit events for %q = %+v, want exactly one", kind, result, candidateKey, events)
	}
	return matching[0]
}

func requireAssetPinIntegrationReceipt(t *testing.T, ctx context.Context, store *storage.FlatSQLStore, rawToken, runID string, consumedAt time.Time) {
	t.Helper()
	digest := assetPinIntegrationTokenDigest(rawToken)
	receipt, found, err := store.FindAssetOIDCReceipt(ctx, digest)
	if err != nil || !found {
		t.Fatalf("find OIDC digest receipt for run %s = %+v, %v, %v; want present", runID, receipt, found, err)
	}
	wantWorkflow := assetPinIntegrationPinWorkflow
	if runID == "1004" {
		wantWorkflow = assetPinIntegrationDecisionWorkflow
	}
	wantExpiry := time.Date(2099, time.January, 1, 0, 0, 0, 0, time.UTC)
	if receipt.Digest != digest || receipt.Repository != assetPinIntegrationRepository || receipt.Ref != assetPinIntegrationRef ||
		receipt.WorkflowRef != wantWorkflow || receipt.Actor != assetPinIntegrationActor || receipt.RunID != runID ||
		receipt.RunAttempt != "1" || receipt.SHA != assetPinIntegrationCommitSHA || !receipt.ExpiresAt.Equal(wantExpiry) ||
		!receipt.ConsumedAt.Equal(consumedAt) {
		t.Fatalf("OIDC receipt for run %s = %+v, want digest-only GitHub-shaped audit receipt", runID, receipt)
	}
}

func requireAssetPinIntegrationLifecycleAudit(t *testing.T, events []storage.AssetPinAuditEvent, approvedCandidate, expiringCandidate, decisionSHA string) {
	t.Helper()
	want := map[string]int{
		approvedCandidate + "|asset_pin_upload|pinned":               1,
		expiringCandidate + "|asset_pin_upload|deduplicated":         1,
		approvedCandidate + "|asset_reference_state|review_open":     1,
		approvedCandidate + "|asset_reference_state|approved":        1,
		expiringCandidate + "|asset_pin_retention_abandon|abandoned": 1,
		expiringCandidate + "|asset_pin_retention_delete|deleted":    1,
	}
	got := make(map[string]int, len(want))
	wantClaims := map[string]struct {
		workflow string
		runID    string
	}{
		approvedCandidate + "|asset_pin_upload|pinned":           {workflow: assetPinIntegrationPinWorkflow, runID: "1001"},
		expiringCandidate + "|asset_pin_upload|deduplicated":     {workflow: assetPinIntegrationPinWorkflow, runID: "1002"},
		approvedCandidate + "|asset_reference_state|review_open": {workflow: assetPinIntegrationPinWorkflow, runID: "1003"},
		approvedCandidate + "|asset_reference_state|approved":    {workflow: assetPinIntegrationDecisionWorkflow, runID: "1004"},
	}
	for _, event := range events {
		key := event.CandidateKey + "|" + event.Kind + "|" + event.Result
		got[key]++
		if event.CID != assetPinIntegrationFixtureCID || event.SHA256 != assetPinIntegrationFixtureSHA || event.ByteCount != assetPinIntegrationFixtureBytes {
			t.Fatalf("lifecycle audit identity = %+v, want exact fixture CID/SHA/bytes", event)
		}
		if len(event.EventID) != 64 || len(event.MutationDigest) != 64 {
			t.Fatalf("lifecycle audit IDs = event %q mutation %q, want stable SHA-256 identities", event.EventID, event.MutationDigest)
		}
		if claims, ok := wantClaims[key]; ok {
			if len(event.TokenDigest) != 64 || event.Repository != assetPinIntegrationRepository || event.Ref != assetPinIntegrationRef ||
				event.WorkflowRef != claims.workflow || event.Actor != assetPinIntegrationActor || event.WorkflowRunID != claims.runID ||
				event.RunAttempt != "1" || event.CommitSHA != assetPinIntegrationCommitSHA {
				t.Fatalf("GitHub-shaped lifecycle audit receipt = %+v, want workflow %q run %q with token digest", event, claims.workflow, claims.runID)
			}
		} else if event.TokenDigest != "" || event.Repository != "" || event.WorkflowRef != "" || event.Actor != "" || event.CommitSHA != "" {
			t.Fatalf("retention lifecycle audit contains workflow credential material: %+v", event)
		}
		switch key {
		case approvedCandidate + "|asset_reference_state|review_open":
			if event.Detail != `{"decisionSha256":"","issueNumber":4242,"state":"review_open"}` || len(event.TokenDigest) != 64 {
				t.Fatalf("review-open audit = %+v, want issue-bound token-digest receipt", event)
			}
		case approvedCandidate + "|asset_reference_state|approved":
			wantDetail := fmt.Sprintf(`{"decisionSha256":"%s","issueNumber":4242,"state":"approved"}`, decisionSHA)
			if event.Detail != wantDetail || len(event.TokenDigest) != 64 {
				t.Fatalf("approval audit = %+v, want canonical decision receipt %s", event, wantDetail)
			}
		case expiringCandidate + "|asset_pin_retention_abandon|abandoned",
			expiringCandidate + "|asset_pin_retention_delete|deleted":
			if event.TokenDigest != "" {
				t.Fatalf("retention audit unexpectedly contains a token digest: %+v", event)
			}
		}
	}
	if len(events) != len(want) {
		t.Fatalf("lifecycle audit event count = %d, want %d; events=%+v", len(events), len(want), events)
	}
	for key, count := range want {
		if got[key] != count {
			t.Fatalf("lifecycle audit count for %q = %d, want %d; all=%+v", key, got[key], count, got)
		}
	}
}

func assertAssetPinIntegrationNoRawTokens(t *testing.T, payload []byte, rawTokens []string) {
	t.Helper()
	for _, rawToken := range rawTokens {
		if rawToken != "" && bytes.Contains(payload, []byte(rawToken)) {
			t.Fatal("durable or returned asset-pin data leaked a raw OIDC token")
		}
	}
}

func assertAssetPinIntegrationTreeHasNoRawTokens(t *testing.T, root string, rawTokens []string) {
	t.Helper()
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		payload, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, rawToken := range rawTokens {
			if rawToken != "" && bytes.Contains(payload, []byte(rawToken)) {
				return fmt.Errorf("raw OIDC token persisted in %s", filepath.Base(path))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan asset-pin durable tree for raw token leakage: %v", err)
	}
}

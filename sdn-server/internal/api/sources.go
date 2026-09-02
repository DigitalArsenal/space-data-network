package api

// GET /api/v1/data/sources — RESOLVED SOURCES (Watchfloor build step 6).
//
// The dashboard's provenance chips need one server answer that composes the
// joins the store already holds but no code composed: per-source summaries
// (which CARRY producer_peer_id, host-stamped and trustworthy) joined to the
// EPM directory (peer_id -> signed publisher profile -> legal name). The
// client's resolver renders exactly what this returns — the ONE honesty
// locus stays intact, this endpoint just moves the composition server-side
// where the producer identity actually exists.
//
// Honesty contract (owner ruling 2026-09-01):
//   - source-tag: the source slug verbatim when no signed publisher identity
//     can be resolved;
//   - signed: the producer's embedded EPM signature verifies and advertises
//     that same producer id;
//   - bonded: a signed EPM carries a chain address whose configured BondSource
//     reports a positive balance through trust.Evaluator.BondBalance.
//
// Verified domain proofs add evidence sentences; they never create a rung.
// Every row carries plain-language evidence ready for the chip hover.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	standardsEPM "github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	secp256k1ecdsa "github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"

	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/trust"
)

const (
	organizationStateSourceTag = "source-tag"
	organizationStateSigned    = "signed"
	organizationStateBonded    = "bonded"
)

type resolvedOrganization struct {
	Name     string   `json:"name"`
	State    string   `json:"state"`
	Evidence []string `json:"evidence"`
	PeerID   string   `json:"peer_id,omitempty"`
	EPMCID   string   `json:"epm_cid,omitempty"`
}

type resolvedSourceRow struct {
	Schema         string                `json:"schema"`
	ProviderID     string                `json:"provider_id"`
	SourceName     string                `json:"source_name"`
	BatchID        string                `json:"batch_id,omitempty"`
	ProducerPeerID string                `json:"producer_peer_id,omitempty"`
	Count          int64                 `json:"count"`
	TotalBytes     int64                 `json:"total_bytes"`
	Organization   *resolvedOrganization `json:"organization"`
}

type resolvedSourcesResponse struct {
	AsOf    string              `json:"as_of"`
	Sources []resolvedSourceRow `json:"sources"`
}

// RegisterResolvedSourcesRoute mounts the resolved-sources lane beside the
// other data routes; the admin wall's standing policy applies unchanged (this
// endpoint invents no auth semantics — spec rule: nothing here gets ahead of
// the security roadmap).
func (h *DataQueryHandler) RegisterResolvedSourcesRoute(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/data/sources", h.handleResolvedSources)
}

// resolveProducerOrganization answers the highest honest rung for one source.
// evaluator is optional because BondSource availability is deployment wiring;
// nil and lookup errors intentionally stop at signed.
func resolveProducerOrganization(store *storage.FlatSQLStore, sourceTag, peerID string, evaluator *trust.Evaluator) *resolvedOrganization {
	floor := sourceTagOrganization(sourceTag)
	peerID = strings.TrimSpace(peerID)
	if peerID == "" || store == nil {
		return floor
	}
	records, err := store.QueryDirectory(storage.DirectoryQuery{PeerID: peerID, Limit: 1})
	if err != nil || len(records) == 0 {
		return floor
	}
	rec := records[0]
	epmRecord, ok := verifiedPublisherEPM(rec, peerID)
	if !ok {
		return floor
	}

	name := strings.TrimSpace(string(epmRecord.LEGAL_NAME()))
	if name == "" {
		name = strings.TrimSpace(string(epmRecord.DN()))
	}
	if name == "" {
		return floor
	}
	resolved := &resolvedOrganization{
		Name:  name,
		State: organizationStateSigned,
		Evidence: []string{
			"The publisher profile signature verifies for " + peerID + ".",
			"The verified publisher profile names " + name + ".",
		},
		PeerID: peerID,
		EPMCID: rec.EPMCID,
	}

	if evaluator != nil {
		proof := new(standardsEPM.ChainProof)
		for i := 0; i < epmRecord.CHAIN_PROOFSLength(); i++ {
			if !epmRecord.CHAIN_PROOFS(proof, i) {
				continue
			}
			address := strings.TrimSpace(string(proof.ADDRESS()))
			if address == "" || evaluator.BondBalance(address) <= 0 {
				continue
			}
			resolved.State = organizationStateBonded
			resolved.Evidence = append(resolved.Evidence,
				"A linked chain address reports a positive bond balance.")
			break
		}
	}

	for _, domain := range verifiedPublisherDomains(epmRecord, peerID, time.Now()) {
		resolved.Evidence = append(resolved.Evidence,
			"A verified domain-control proof links this publisher to "+domain+".")
	}
	return resolved
}

func sourceTagOrganization(sourceTag string) *resolvedOrganization {
	if sourceTag == "" {
		sourceTag = "unknown"
	}
	return &resolvedOrganization{
		Name:  sourceTag,
		State: organizationStateSourceTag,
		Evidence: []string{
			"Records carry the source tag " + sourceTag + ", but no verified publisher identity is available.",
			"Treat this source as unconfirmed.",
		},
	}
}

func verifiedPublisherEPM(rec storage.DirectoryRecord, peerID string) (*standardsEPM.EPM, bool) {
	var stored struct {
		EPMBase64 string `json:"epm_base64"`
	}
	if json.Unmarshal([]byte(rec.EPMJSON), &stored) != nil || strings.TrimSpace(stored.EPMBase64) == "" {
		return nil, false
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(stored.EPMBase64))
	if err != nil || !standardsEPM.SizePrefixedEPMBufferHasIdentifier(data) {
		return nil, false
	}
	if err := epm.VerifyEPMSignature(data); err != nil {
		return nil, false
	}
	advertisedPeerID, err := epm.PeerIDFromEPM(data)
	if err != nil || strings.TrimSpace(advertisedPeerID) != strings.TrimSpace(peerID) {
		return nil, false
	}
	return standardsEPM.GetSizePrefixedRootAsEPM(data, 0), true
}

func verifiedPublisherDomains(record *standardsEPM.EPM, peerID string, now time.Time) []string {
	if record == nil {
		return nil
	}
	seen := map[string]struct{}{}
	proof := new(standardsEPM.DomainProof)
	for i := 0; i < record.DOMAIN_PROOFSLength(); i++ {
		if !record.DOMAIN_PROOFS(proof, i) {
			continue
		}
		domain, ok := verifyPublisherDomainProof(record, proof, peerID, now)
		if ok {
			seen[domain] = struct{}{}
		}
	}
	domains := make([]string, 0, len(seen))
	for domain := range seen {
		domains = append(domains, domain)
	}
	sort.Strings(domains)
	return domains
}

func verifyPublisherDomainProof(record *standardsEPM.EPM, proof *standardsEPM.DomainProof, peerID string, now time.Time) (string, bool) {
	domain := strings.TrimSpace(string(proof.DOMAIN()))
	publicHex := strings.TrimSpace(string(proof.PUBLIC_KEY()))
	algorithm := strings.ToLower(strings.TrimSpace(string(proof.ALGORITHM())))
	if algorithm == "" {
		algorithm = "ed25519"
	}
	if !canonicalDomain(domain) || publicHex == "" || publicHex != strings.ToLower(publicHex) ||
		!epmCarriesSigningKey(record, publicHex, algorithm) {
		return "", false
	}

	payload, err := decodeCanonicalHex(string(proof.SIGNED_PAYLOAD()))
	if err != nil {
		return "", false
	}
	lines := strings.Split(string(payload), "\n")
	if len(lines) != 7 || lines[0] != "sdn-domain-proof/1" || lines[6] != "" ||
		lines[1] != "domain="+domain || lines[2] != "key="+algorithm+":"+publicHex ||
		lines[3] != "peerid="+peerID {
		return "", false
	}
	issued, err := parseCanonicalProofTime(lines[4], "issued=")
	if err != nil {
		return "", false
	}
	expires, err := parseCanonicalProofTime(lines[5], "expires=")
	if err != nil {
		return "", false
	}
	canonical := fmt.Sprintf("sdn-domain-proof/1\ndomain=%s\nkey=%s:%s\npeerid=%s\nissued=%d\nexpires=%d\n",
		domain, algorithm, publicHex, peerID, issued, expires)
	if !bytes.Equal(payload, []byte(canonical)) {
		return "", false
	}
	const clockSkew = int64(300)
	nowUnix := now.Unix()
	if issued > nowUnix+clockSkew || (expires != 0 && expires < nowUnix-clockSkew) {
		return "", false
	}

	publicKey, err := decodeCanonicalHex(publicHex)
	if err != nil {
		return "", false
	}
	signature, err := decodeCanonicalHex(string(proof.SIGNATURE()))
	if err != nil || !verifyDomainSignature(algorithm, publicKey, payload, signature) {
		return "", false
	}
	return domain, true
}

func epmCarriesSigningKey(record *standardsEPM.EPM, publicHex, algorithm string) bool {
	key := new(standardsEPM.CryptoKey)
	for i := 0; i < record.KEYSLength(); i++ {
		if !record.KEYS(key, i) || key.KEY_TYPE() != standardsEPM.KeyTypeSigning ||
			strings.TrimSpace(string(key.PUBLIC_KEY())) != publicHex {
			continue
		}
		keyAlgorithm := strings.ToLower(strings.TrimSpace(string(key.ALGORITHM())))
		if keyAlgorithm == "" {
			keyAlgorithm = strings.ToLower(strings.TrimSpace(string(key.ADDRESS_TYPE())))
		}
		if keyAlgorithm == "" {
			keyAlgorithm = "ed25519"
		}
		return keyAlgorithm == algorithm
	}
	return false
}

func parseCanonicalProofTime(line, prefix string) (int64, error) {
	if !strings.HasPrefix(line, prefix) {
		return 0, fmt.Errorf("missing %s", prefix)
	}
	value := strings.TrimPrefix(line, prefix)
	if value == "" || (len(value) > 1 && value[0] == '0') {
		return 0, fmt.Errorf("non-canonical %s", prefix)
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("invalid %s", prefix)
	}
	return parsed, nil
}

func decodeCanonicalHex(value string) ([]byte, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || trimmed != strings.ToLower(trimmed) || strings.HasPrefix(trimmed, "0x") {
		return nil, fmt.Errorf("non-canonical hex")
	}
	return hex.DecodeString(trimmed)
}

func verifyDomainSignature(algorithm string, publicKey, payload, signature []byte) bool {
	switch algorithm {
	case "ed25519":
		return len(publicKey) == ed25519.PublicKeySize && len(signature) == ed25519.SignatureSize &&
			ed25519.Verify(ed25519.PublicKey(publicKey), payload, signature)
	case "secp256k1":
		key, err := secp256k1.ParsePubKey(publicKey)
		if err != nil {
			return false
		}
		sig, err := secp256k1ecdsa.ParseDERSignature(signature)
		if err != nil {
			return false
		}
		digest := sha256.Sum256(payload)
		return sig.Verify(digest[:], key)
	default:
		return false
	}
}

func canonicalDomain(domain string) bool {
	if domain == "" || domain != strings.ToLower(domain) || strings.HasSuffix(domain, ".") || strings.Contains(domain, "..") {
		return false
	}
	for _, ch := range domain {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '.' {
			continue
		}
		return false
	}
	return true
}

func (h *DataQueryHandler) handleResolvedSources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	if !h.ensureStore(w) {
		return
	}
	summary, err := h.store.DataSummary()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	// One directory lookup per distinct producing peer, not per source row.
	orgByPeer := map[string]*resolvedOrganization{}
	unresolvedPeers := map[string]bool{}
	rows := make([]resolvedSourceRow, 0, len(summary.Sources))
	for _, src := range summary.Sources {
		sourceTag := src.SourceName
		if sourceTag == "" {
			sourceTag = src.ProviderID
		}
		org, seen := orgByPeer[src.ProducerPeerID]
		if unresolvedPeers[src.ProducerPeerID] {
			org = sourceTagOrganization(sourceTag)
		} else if !seen {
			org = resolveProducerOrganization(h.store, sourceTag, src.ProducerPeerID, nil)
			if org.State == organizationStateSourceTag {
				unresolvedPeers[src.ProducerPeerID] = true
			} else {
				orgByPeer[src.ProducerPeerID] = org
			}
		}
		rows = append(rows, resolvedSourceRow{
			Schema:         src.SchemaName,
			ProviderID:     src.ProviderID,
			SourceName:     src.SourceName,
			BatchID:        src.BatchID,
			ProducerPeerID: src.ProducerPeerID,
			Count:          src.Count,
			TotalBytes:     src.TotalBytes,
			Organization:   org,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Schema != rows[j].Schema {
			return rows[i].Schema < rows[j].Schema
		}
		return rows[i].Count > rows[j].Count
	})
	writeJSON(w, http.StatusOK, resolvedSourcesResponse{
		AsOf:    time.Now().UTC().Format(time.RFC3339),
		Sources: rows,
	})
}

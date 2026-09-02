package storefront

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"

	sdsepm "github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	sdspnm "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"
	sdsstf "github.com/DigitalArsenal/spacedatastandards.org/lib/go/STF"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

type pendingListingAnnouncement struct {
	SourcePeer peer.ID
	STFCID     string
	DPMCID     string
	PNMBytes   []byte
}

type pendingListingSTF struct {
	SourcePeer peer.ID
	Bytes      []byte
}

func (s *Service) runListingSubscription(ctx context.Context) {
	defer close(s.listingDone)
	for {
		message, err := s.listingSub.Next(ctx)
		if err != nil {
			return
		}
		origin := peer.ID(message.GetFrom())
		if origin.String() == s.peerID {
			continue
		}
		s.consumeListingPublication(message.Data, origin)
	}
}

func (s *Service) consumeListingPublication(data []byte, source peer.ID) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Warnf("Rejected malformed storefront publication from %s: %v", source, recovered)
		}
	}()
	if sdspnm.SizePrefixedPNMBufferHasIdentifier(data) {
		pnm := sdspnm.GetSizePrefixedRootAsPNM(data, 0)
		cid := strings.TrimSpace(string(pnm.CID()))
		if cid == "" {
			return
		}
		var dpmCID string
		if parsed, err := url.Parse(string(pnm.MULTIFORMAT_ADDRESS())); err == nil {
			dpmCID = strings.TrimSpace(parsed.Query().Get("dpm"))
		}
		s.mu.Lock()
		if len(s.pendingListings) < 1024 {
			s.pendingListings[cid] = pendingListingAnnouncement{
				SourcePeer: source, STFCID: cid, DPMCID: dpmCID,
				PNMBytes: append([]byte(nil), data...),
			}
		}
		orphan, hasOrphan := s.pendingSTF[cid]
		if hasOrphan && orphan.SourcePeer == source {
			delete(s.pendingSTF, cid)
		}
		s.mu.Unlock()
		if hasOrphan && orphan.SourcePeer == source {
			s.consumeListingPublication(orphan.Bytes, source)
		}
		return
	}
	if sdsstf.STFBufferHasIdentifier(data) {
		cid := storage.ComputeCID(data)
		s.mu.RLock()
		pending, ok := s.pendingListings[cid]
		s.mu.RUnlock()
		if !ok || pending.SourcePeer != source {
			s.mu.Lock()
			if len(s.pendingSTF) < 1024 {
				s.pendingSTF[cid] = pendingListingSTF{SourcePeer: source, Bytes: append([]byte(nil), data...)}
			}
			s.mu.Unlock()
			return
		}
		listing, err := decodeListingRecord(data)
		if err != nil {
			log.Warnf("Rejected storefront STF from %s: %v", source, err)
			return
		}
		if listing.ProviderPeerID != source.String() {
			log.Warnf("Rejected storefront STF from %s: provider is %s", source, listing.ProviderPeerID)
			return
		}
		publicKey, err := s.listingSigningPublicKey(source, listing.ProviderEPMCID)
		if err != nil {
			log.Warnf("Rejected storefront publication from %s: %v", source, err)
			return
		}
		if err := VerifyListingPNM(pending.PNMBytes, publicKey); err != nil {
			log.Warnf("Rejected storefront PNM from %s: %v", source, err)
			return
		}
		if err := VerifySTFBytes(data, publicKey); err != nil {
			log.Warnf("Rejected storefront STF from %s: %v", source, err)
			return
		}
		if _, err := s.store.StoreReceivedListing(listing, data, source.String()); err != nil {
			log.Warnf("Failed to index storefront STF from %s: %v", source, err)
			return
		}
		s.mu.Lock()
		delete(s.pendingListings, cid)
		s.mu.Unlock()
	}
}

func (s *Service) listingSigningPublicKey(source peer.ID, epmCID string) (ed25519.PublicKey, error) {
	epmCID = strings.TrimSpace(epmCID)
	if epmCID != "" {
		if data, err := s.store.flatStore.Get("EPM.fbs", epmCID); err == nil && storage.ComputeCID(data) == epmCID {
			if key, err := ed25519SigningKeyFromEPM(data); err == nil {
				return key, nil
			}
		}
	}
	// Legacy/non-HD nodes use the Ed25519 libp2p identity directly. This is a
	// valid EPM signing key only when the peer ID itself exposes those bytes;
	// HD nodes use a secp256k1 peer identity and therefore cannot take this path.
	if key, err := listingPublicKeyForPeer(source); err == nil {
		return key, nil
	}
	return nil, fmt.Errorf("no Ed25519 signing key found in provider EPM %s for %s", epmCID, source)
}

func ed25519SigningKeyFromEPM(data []byte) (ed25519.PublicKey, error) {
	var epm *sdsepm.EPM
	switch {
	case sdsepm.SizePrefixedEPMBufferHasIdentifier(data):
		epm = sdsepm.GetSizePrefixedRootAsEPM(data, 0)
	case sdsepm.EPMBufferHasIdentifier(data):
		epm = sdsepm.GetRootAsEPM(data, 0)
	default:
		return nil, fmt.Errorf("EPM buffer missing $EPM file identifier")
	}
	for i := 0; i < epm.KEYSLength(); i++ {
		var key sdsepm.CryptoKey
		if !epm.KEYS(&key, i) || !strings.EqualFold(key.KEY_TYPE().String(), "Signing") {
			continue
		}
		algorithm := strings.TrimSpace(string(key.ALGORITHM()))
		addressType := strings.TrimSpace(string(key.ADDRESS_TYPE()))
		if !strings.EqualFold(algorithm, "Ed25519") && !strings.EqualFold(addressType, "Ed25519") {
			continue
		}
		raw, err := hex.DecodeString(strings.TrimSpace(string(key.PUBLIC_KEY())))
		if err == nil && len(raw) == ed25519.PublicKeySize {
			return ed25519.PublicKey(raw), nil
		}
	}
	return nil, fmt.Errorf("EPM has no Ed25519 signing key")
}

func listingPublicKeyForPeer(id peer.ID) (ed25519.PublicKey, error) {
	key, err := id.ExtractPublicKey()
	if err != nil {
		return nil, err
	}
	raw, err := key.Raw()
	if err != nil {
		return nil, err
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, errUnsupportedListingPeerKey{id: id, length: len(raw)}
	}
	return ed25519.PublicKey(append([]byte(nil), raw...)), nil
}

type errUnsupportedListingPeerKey struct {
	id     peer.ID
	length int
}

func (e errUnsupportedListingPeerKey) Error() string {
	return "peer " + e.id.String() + " does not expose a 32-byte Ed25519 signing key"
}

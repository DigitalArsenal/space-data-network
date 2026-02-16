// Package epm provides EPM (Entity Profile Message) lifecycle management.
// It creates, stores, and serves the node's identity card (EPM), which
// contains cryptographic keys, contact information, and network addresses.
package epm

import (
	"encoding/hex"
	"fmt"
	"sync"

	flatbuffers "github.com/google/flatbuffers/go"
	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/vcard"
	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

var log = logging.Logger("sdn-epm")

// Service manages the node's EPM (Entity Profile Message).
type Service struct {
	identity *wasm.DerivedIdentity
	registry *peers.Registry
	peerID   peer.ID
	xpub     string
	dataDir  string

	epmBytes []byte // current node EPM (size-prefixed FlatBuffer)
	profile  *Profile

	mu sync.RWMutex
}

// NewService creates a new EPM service.
// identity may be nil if using random keys (EPM will lack HD wallet fields).
func NewService(identity *wasm.DerivedIdentity, registry *peers.Registry, peerID peer.ID, xpub, dataDir string) *Service {
	return &Service{
		identity: identity,
		registry: registry,
		peerID:   peerID,
		xpub:     xpub,
		dataDir:  dataDir,
	}
}

// Init loads or creates the node's EPM profile and builds the initial EPM.
func (s *Service) Init() error {
	// Load existing profile or create default
	profile, err := LoadProfile(s.dataDir)
	if err != nil {
		log.Infof("No existing EPM profile, creating default")
		profile = s.defaultProfile()
	}
	s.profile = profile

	// Build EPM from profile + identity
	if err := s.rebuildEPM(); err != nil {
		return fmt.Errorf("failed to build node EPM: %w", err)
	}

	log.Infof("EPM service initialized (PeerID=%s, hasIdentity=%v)", s.peerID, s.identity != nil)
	return nil
}

// GetNodeEPM returns the current EPM as a size-prefixed FlatBuffer.
func (s *Service) GetNodeEPM() []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.epmBytes == nil {
		return nil
	}
	out := make([]byte, len(s.epmBytes))
	copy(out, s.epmBytes)
	return out
}

// GetNodeVCard returns the node's EPM as a vCard 4.0 string.
func (s *Service) GetNodeVCard() (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.epmBytes == nil {
		return "", fmt.Errorf("no EPM available")
	}
	return vcard.EPMToVCard(s.epmBytes)
}

// GetNodeQR returns a QR code PNG of the node's vCard.
func (s *Service) GetNodeQR(size int) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.epmBytes == nil {
		return nil, fmt.Errorf("no EPM available")
	}
	return vcard.EPMToQR(s.epmBytes, size)
}

// GetNodeProfile returns the current editable profile.
func (s *Service) GetNodeProfile() *Profile {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.profile == nil {
		return nil
	}
	cp := *s.profile
	return &cp
}

// GetNodeEPMJSON returns the EPM as a JSON-friendly structure.
func (s *Service) GetNodeEPMJSON() map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.epmBytes == nil {
		return nil
	}

	epm := EPM.GetSizePrefixedRootAsEPM(s.epmBytes, 0)
	result := make(map[string]interface{})

	if dn := epm.DN(); dn != nil {
		result["dn"] = string(dn)
	}
	if ln := epm.LEGAL_NAME(); ln != nil {
		result["legal_name"] = string(ln)
	}
	if fn := epm.FAMILY_NAME(); fn != nil {
		result["family_name"] = string(fn)
	}
	if gn := epm.GIVEN_NAME(); gn != nil {
		result["given_name"] = string(gn)
	}
	if an := epm.ADDITIONAL_NAME(); an != nil {
		result["additional_name"] = string(an)
	}
	if hp := epm.HONORIFIC_PREFIX(); hp != nil {
		result["honorific_prefix"] = string(hp)
	}
	if hs := epm.HONORIFIC_SUFFIX(); hs != nil {
		result["honorific_suffix"] = string(hs)
	}
	if jt := epm.JOB_TITLE(); jt != nil {
		result["job_title"] = string(jt)
	}
	if oc := epm.OCCUPATION(); oc != nil {
		result["occupation"] = string(oc)
	}
	if em := epm.EMAIL(); em != nil {
		result["email"] = string(em)
	}
	if tel := epm.TELEPHONE(); tel != nil {
		result["telephone"] = string(tel)
	}

	// Address
	addr := new(EPM.Address)
	if epm.ADDRESS(addr) != nil {
		addrMap := make(map[string]string)
		if v := addr.COUNTRY(); v != nil {
			addrMap["country"] = string(v)
		}
		if v := addr.REGION(); v != nil {
			addrMap["region"] = string(v)
		}
		if v := addr.LOCALITY(); v != nil {
			addrMap["locality"] = string(v)
		}
		if v := addr.POSTAL_CODE(); v != nil {
			addrMap["postal_code"] = string(v)
		}
		if v := addr.STREET(); v != nil {
			addrMap["street"] = string(v)
		}
		if v := addr.POST_OFFICE_BOX_NUMBER(); v != nil {
			addrMap["po_box"] = string(v)
		}
		if len(addrMap) > 0 {
			result["address"] = addrMap
		}
	}

	// Alternate names
	if n := epm.ALTERNATE_NAMESLength(); n > 0 {
		names := make([]string, 0, n)
		for i := 0; i < n; i++ {
			if v := epm.ALTERNATE_NAMES(i); v != nil {
				names = append(names, string(v))
			}
		}
		result["alternate_names"] = names
	}

	// Keys
	key := new(EPM.CryptoKey)
	if n := epm.KEYSLength(); n > 0 {
		keys := make([]map[string]interface{}, 0, n)
		for i := 0; i < n; i++ {
			if epm.KEYS(key, i) {
				k := make(map[string]interface{})
				if v := key.PUBLIC_KEY(); v != nil {
					k["public_key"] = string(v)
				}
				if v := key.XPUB(); v != nil {
					k["xpub"] = string(v)
				}
				if v := key.KEY_ADDRESS(); v != nil {
					k["key_address"] = string(v)
				}
				if v := key.ADDRESS_TYPE(); v != nil {
					k["address_type"] = string(v)
				}
				switch key.KEY_TYPE() {
				case EPM.KeyTypeSigning:
					k["key_type"] = "signing"
				case EPM.KeyTypeEncryption:
					k["key_type"] = "encryption"
				}
				keys = append(keys, k)
			}
		}
		result["keys"] = keys
	}

	// Multiformat addresses
	if n := epm.MULTIFORMAT_ADDRESSLength(); n > 0 {
		addrs := make([]string, 0, n)
		for i := 0; i < n; i++ {
			if v := epm.MULTIFORMAT_ADDRESS(i); v != nil {
				addrs = append(addrs, string(v))
			}
		}
		result["multiformat_address"] = addrs
	}

	// Add identity metadata
	result["peer_id"] = s.peerID.String()

	return result
}

// UpdateProfile updates the node's EPM profile and rebuilds the EPM.
func (s *Service) UpdateProfile(profile *Profile) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.profile = profile
	if err := s.rebuildEPMLocked(); err != nil {
		return fmt.Errorf("failed to rebuild EPM: %w", err)
	}

	// Persist profile
	if err := SaveProfile(s.dataDir, profile); err != nil {
		log.Warnf("Failed to persist EPM profile: %v", err)
	}

	return nil
}

// rebuildEPM builds EPM bytes from the current profile + identity.
func (s *Service) rebuildEPM() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rebuildEPMLocked()
}

// rebuildEPMLocked builds EPM bytes. Caller must hold s.mu.
func (s *Service) rebuildEPMLocked() error {
	builder := flatbuffers.NewBuilder(2048)

	p := s.profile
	if p == nil {
		p = &Profile{}
	}

	// Create string offsets
	var dnOff, legalNameOff, familyNameOff, givenNameOff flatbuffers.UOffsetT
	var additionalNameOff, prefixOff, suffixOff flatbuffers.UOffsetT
	var jobTitleOff, occupationOff, emailOff, telephoneOff flatbuffers.UOffsetT

	if p.DN != "" {
		dnOff = builder.CreateString(p.DN)
	}
	if p.LegalName != "" {
		legalNameOff = builder.CreateString(p.LegalName)
	}
	if p.FamilyName != "" {
		familyNameOff = builder.CreateString(p.FamilyName)
	}
	if p.GivenName != "" {
		givenNameOff = builder.CreateString(p.GivenName)
	}
	if p.AdditionalName != "" {
		additionalNameOff = builder.CreateString(p.AdditionalName)
	}
	if p.HonorificPrefix != "" {
		prefixOff = builder.CreateString(p.HonorificPrefix)
	}
	if p.HonorificSuffix != "" {
		suffixOff = builder.CreateString(p.HonorificSuffix)
	}
	if p.JobTitle != "" {
		jobTitleOff = builder.CreateString(p.JobTitle)
	}
	if p.Occupation != "" {
		occupationOff = builder.CreateString(p.Occupation)
	}
	if p.Email != "" {
		emailOff = builder.CreateString(p.Email)
	}
	if p.Telephone != "" {
		telephoneOff = builder.CreateString(p.Telephone)
	}

	// Address
	var addressOff flatbuffers.UOffsetT
	if p.Address != nil && !p.Address.IsEmpty() {
		var countryOff, regionOff, localityOff, postalOff, streetOff, poBoxOff flatbuffers.UOffsetT
		if p.Address.Country != "" {
			countryOff = builder.CreateString(p.Address.Country)
		}
		if p.Address.Region != "" {
			regionOff = builder.CreateString(p.Address.Region)
		}
		if p.Address.Locality != "" {
			localityOff = builder.CreateString(p.Address.Locality)
		}
		if p.Address.PostalCode != "" {
			postalOff = builder.CreateString(p.Address.PostalCode)
		}
		if p.Address.Street != "" {
			streetOff = builder.CreateString(p.Address.Street)
		}
		if p.Address.POBox != "" {
			poBoxOff = builder.CreateString(p.Address.POBox)
		}

		EPM.AddressStart(builder)
		if countryOff != 0 {
			EPM.AddressAddCOUNTRY(builder, countryOff)
		}
		if regionOff != 0 {
			EPM.AddressAddREGION(builder, regionOff)
		}
		if localityOff != 0 {
			EPM.AddressAddLOCALITY(builder, localityOff)
		}
		if postalOff != 0 {
			EPM.AddressAddPOSTAL_CODE(builder, postalOff)
		}
		if streetOff != 0 {
			EPM.AddressAddSTREET(builder, streetOff)
		}
		if poBoxOff != 0 {
			EPM.AddressAddPOST_OFFICE_BOX_NUMBER(builder, poBoxOff)
		}
		addressOff = EPM.AddressEnd(builder)
	}

	// Alternate names
	var altNamesOff flatbuffers.UOffsetT
	if len(p.AlternateNames) > 0 {
		offsets := make([]flatbuffers.UOffsetT, len(p.AlternateNames))
		for i, name := range p.AlternateNames {
			offsets[i] = builder.CreateString(name)
		}
		EPM.EPMStartALTERNATE_NAMESVector(builder, len(offsets))
		for i := len(offsets) - 1; i >= 0; i-- {
			builder.PrependUOffsetT(offsets[i])
		}
		altNamesOff = builder.EndVector(len(offsets))
	}

	// Build CryptoKey entries from identity
	var keysOff flatbuffers.UOffsetT
	var keyOffsets []flatbuffers.UOffsetT

	if s.identity != nil {
		// Signing key (Ed25519)
		sigPubBytes, _ := s.identity.SigningPubKey.Raw()
		sigPubHex := hex.EncodeToString(sigPubBytes)

		sigPubOff := builder.CreateString(sigPubHex)
		var sigXpubOff flatbuffers.UOffsetT
		if s.xpub != "" {
			sigXpubOff = builder.CreateString(s.xpub)
		}
		sigAddrTypeOff := builder.CreateString("ed25519")
		sigPathOff := builder.CreateString(s.identity.SigningKeyPath)

		EPM.CryptoKeyStart(builder)
		EPM.CryptoKeyAddPUBLIC_KEY(builder, sigPubOff)
		if sigXpubOff != 0 {
			EPM.CryptoKeyAddXPUB(builder, sigXpubOff)
		}
		EPM.CryptoKeyAddADDRESS_TYPE(builder, sigAddrTypeOff)
		EPM.CryptoKeyAddKEY_ADDRESS(builder, sigPathOff)
		EPM.CryptoKeyAddKEY_TYPE(builder, EPM.KeyTypeSigning)
		sigKeyOff := EPM.CryptoKeyEnd(builder)
		keyOffsets = append(keyOffsets, sigKeyOff)

		// Encryption key (X25519)
		encPubHex := hex.EncodeToString(s.identity.EncryptionPub)
		encPubOff := builder.CreateString(encPubHex)
		encAddrTypeOff := builder.CreateString("x25519")
		encPathOff := builder.CreateString(s.identity.EncryptionKeyPath)

		EPM.CryptoKeyStart(builder)
		EPM.CryptoKeyAddPUBLIC_KEY(builder, encPubOff)
		EPM.CryptoKeyAddADDRESS_TYPE(builder, encAddrTypeOff)
		EPM.CryptoKeyAddKEY_ADDRESS(builder, encPathOff)
		EPM.CryptoKeyAddKEY_TYPE(builder, EPM.KeyTypeEncryption)
		encKeyOff := EPM.CryptoKeyEnd(builder)
		keyOffsets = append(keyOffsets, encKeyOff)
	}

	if len(keyOffsets) > 0 {
		EPM.EPMStartKEYSVector(builder, len(keyOffsets))
		for i := len(keyOffsets) - 1; i >= 0; i-- {
			builder.PrependUOffsetT(keyOffsets[i])
		}
		keysOff = builder.EndVector(len(keyOffsets))
	}

	// Multiformat addresses (IPNS)
	var multiAddrOff flatbuffers.UOffsetT
	peerIDStr := s.peerID.String()
	ipnsAddr := "/ipns/" + peerIDStr
	addrOff := builder.CreateString(ipnsAddr)
	EPM.EPMStartMULTIFORMAT_ADDRESSVector(builder, 1)
	builder.PrependUOffsetT(addrOff)
	multiAddrOff = builder.EndVector(1)

	// Build EPM table
	EPM.EPMStart(builder)
	if dnOff != 0 {
		EPM.EPMAddDN(builder, dnOff)
	}
	if legalNameOff != 0 {
		EPM.EPMAddLEGAL_NAME(builder, legalNameOff)
	}
	if familyNameOff != 0 {
		EPM.EPMAddFAMILY_NAME(builder, familyNameOff)
	}
	if givenNameOff != 0 {
		EPM.EPMAddGIVEN_NAME(builder, givenNameOff)
	}
	if additionalNameOff != 0 {
		EPM.EPMAddADDITIONAL_NAME(builder, additionalNameOff)
	}
	if prefixOff != 0 {
		EPM.EPMAddHONORIFIC_PREFIX(builder, prefixOff)
	}
	if suffixOff != 0 {
		EPM.EPMAddHONORIFIC_SUFFIX(builder, suffixOff)
	}
	if jobTitleOff != 0 {
		EPM.EPMAddJOB_TITLE(builder, jobTitleOff)
	}
	if occupationOff != 0 {
		EPM.EPMAddOCCUPATION(builder, occupationOff)
	}
	if addressOff != 0 {
		EPM.EPMAddADDRESS(builder, addressOff)
	}
	if altNamesOff != 0 {
		EPM.EPMAddALTERNATE_NAMES(builder, altNamesOff)
	}
	if emailOff != 0 {
		EPM.EPMAddEMAIL(builder, emailOff)
	}
	if telephoneOff != 0 {
		EPM.EPMAddTELEPHONE(builder, telephoneOff)
	}
	if keysOff != 0 {
		EPM.EPMAddKEYS(builder, keysOff)
	}
	if multiAddrOff != 0 {
		EPM.EPMAddMULTIFORMAT_ADDRESS(builder, multiAddrOff)
	}
	epmOff := EPM.EPMEnd(builder)

	EPM.FinishSizePrefixedEPMBuffer(builder, epmOff)

	result := make([]byte, len(builder.FinishedBytes()))
	copy(result, builder.FinishedBytes())
	s.epmBytes = result

	return nil
}

// defaultProfile creates a default profile with the node's PeerID as DN.
func (s *Service) defaultProfile() *Profile {
	return &Profile{
		DN: "SDN Node " + s.peerID.ShortString(),
	}
}

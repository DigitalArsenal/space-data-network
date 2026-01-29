// Package peers provides trusted peer registry and management for the SDN.
package peers

import (
	"fmt"
	"strings"

	"github.com/emersion/go-vcard"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// VCardPeerInfo contains peer information extracted from a vCard.
type VCardPeerInfo struct {
	PeerID       peer.ID
	Name         string
	Organization string
	Addrs        []multiaddr.Multiaddr
	Notes        string
	Metadata     map[string]string
}

// SDN vCard extended property names.
const (
	VCardPropPeerID    = "X-SDN-PEER-ID"
	VCardPropMultiaddr = "X-SDN-MULTIADDR"
	VCardPropTrust     = "X-SDN-TRUST-LEVEL"
	VCardPropGroup     = "X-SDN-GROUP"
)

// ParseVCard parses a vCard string and extracts SDN peer information.
// The vCard must contain the X-SDN-PEER-ID extended property.
// Optionally includes X-SDN-MULTIADDR for addresses, X-SDN-TRUST-LEVEL
// for trust level, and X-SDN-GROUP for group membership.
func ParseVCard(vcardData string) (*VCardPeerInfo, error) {
	dec := vcard.NewDecoder(strings.NewReader(vcardData))
	card, err := dec.Decode()
	if err != nil {
		return nil, fmt.Errorf("failed to parse vCard: %w", err)
	}

	return extractPeerFromCard(card)
}

// ParseVCards parses multiple vCards from a string and returns all peer info found.
func ParseVCards(vcardData string) ([]*VCardPeerInfo, error) {
	dec := vcard.NewDecoder(strings.NewReader(vcardData))
	var peers []*VCardPeerInfo

	for {
		card, err := dec.Decode()
		if err != nil {
			break
		}

		info, err := extractPeerFromCard(card)
		if err != nil {
			continue // Skip cards without valid peer info
		}
		peers = append(peers, info)
	}

	if len(peers) == 0 {
		return nil, fmt.Errorf("no valid SDN peer information found in vCard data")
	}

	return peers, nil
}

// extractPeerFromCard extracts SDN peer info from a parsed vCard.
func extractPeerFromCard(card vcard.Card) (*VCardPeerInfo, error) {
	// Extract peer ID (required)
	peerIDField := card.Get(VCardPropPeerID)
	if peerIDField == nil {
		return nil, fmt.Errorf("vCard missing %s property", VCardPropPeerID)
	}

	peerID, err := peer.Decode(peerIDField.Value)
	if err != nil {
		return nil, fmt.Errorf("invalid peer ID in vCard: %w", err)
	}

	info := &VCardPeerInfo{
		PeerID:   peerID,
		Metadata: make(map[string]string),
	}

	// Extract name
	if fn := card.Get(vcard.FieldFormattedName); fn != nil {
		info.Name = fn.Value
	}

	// Extract organization
	if org := card.Get(vcard.FieldOrganization); org != nil {
		info.Organization = org.Value
	}

	// Extract notes
	if note := card.Get(vcard.FieldNote); note != nil {
		info.Notes = note.Value
	}

	// Extract multiaddresses
	for _, field := range card[VCardPropMultiaddr] {
		if addr, err := multiaddr.NewMultiaddr(field.Value); err == nil {
			info.Addrs = append(info.Addrs, addr)
		}
	}

	// Extract trust level as metadata
	if trust := card.Get(VCardPropTrust); trust != nil {
		info.Metadata["trust_level"] = trust.Value
	}

	// Extract groups as metadata
	var groups []string
	for _, field := range card[VCardPropGroup] {
		groups = append(groups, field.Value)
	}
	if len(groups) > 0 {
		info.Metadata["groups"] = strings.Join(groups, ",")
	}

	return info, nil
}

// TrustedPeerToVCard converts a TrustedPeer to vCard format string.
func TrustedPeerToVCard(tp *TrustedPeer) string {
	card := make(vcard.Card)

	// Required vCard fields
	card.SetValue(vcard.FieldVersion, "4.0")

	// Name
	name := tp.Name
	if name == "" {
		name = tp.ID.ShortString()
	}
	card.SetValue(vcard.FieldFormattedName, name)

	// Organization
	if tp.Organization != "" {
		card.SetValue(vcard.FieldOrganization, tp.Organization)
	}

	// Notes
	if tp.Notes != "" {
		card.SetValue(vcard.FieldNote, tp.Notes)
	}

	// SDN-specific fields
	card.SetValue(VCardPropPeerID, tp.ID.String())
	card.SetValue(VCardPropTrust, tp.TrustLevel.String())

	// Multiaddresses
	for _, addr := range tp.Addrs {
		card.Add(VCardPropMultiaddr, &vcard.Field{Value: addr.String()})
	}

	// Groups
	for _, group := range tp.Groups {
		card.Add(VCardPropGroup, &vcard.Field{Value: group})
	}

	var buf strings.Builder
	enc := vcard.NewEncoder(&buf)
	enc.Encode(card)
	return buf.String()
}

// ImportPeerFromVCard parses a vCard and adds the peer to the registry.
// If the peer already exists, it returns ErrPeerAlreadyExists.
func ImportPeerFromVCard(registry *Registry, vcardData string) (*TrustedPeer, error) {
	info, err := ParseVCard(vcardData)
	if err != nil {
		return nil, err
	}

	// Determine trust level from vCard metadata or default to Standard
	trustLevel := Standard
	if tlStr, ok := info.Metadata["trust_level"]; ok {
		if tl, err := ParseTrustLevel(tlStr); err == nil {
			trustLevel = tl
		}
	}

	// Determine groups
	var groups []string
	if groupStr, ok := info.Metadata["groups"]; ok && groupStr != "" {
		groups = strings.Split(groupStr, ",")
	}

	tp := &TrustedPeer{
		ID:           info.PeerID,
		Addrs:        info.Addrs,
		TrustLevel:   trustLevel,
		Name:         info.Name,
		Organization: info.Organization,
		Notes:        info.Notes,
		Groups:       groups,
		VCardData:    vcardData,
	}

	if err := registry.AddPeer(tp); err != nil {
		return nil, err
	}

	return tp, nil
}

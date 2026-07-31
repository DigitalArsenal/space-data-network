package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/vcard"
)

// loadEnrolledPeerEPMs loads operator-managed signed EPM records (*.epm)
// from dir into the peer registry, so an enrolled fleet peer's full crypto
// identity is held from provisioning — even while the peer is offline, and
// immune to the registry-projection/hydration race (owner directive
// 2026-07-31: "on instantiation, once the keys are generated, you have all
// the info you need to get this").
//
// Every file must be a size-prefixed EPM FlatBuffer whose self-signature
// verifies and whose advertised peer id decodes — anything else is skipped
// loudly. Returns how many records were loaded.
func loadEnrolledPeerEPMs(dir string, registry *peers.Registry) int {
	dir = strings.TrimSpace(dir)
	if dir == "" || registry == nil {
		return 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Warnf("peer-epm-enrolment: cannot read %s: %v", dir, err)
		return 0
	}
	loaded := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".epm") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		epmBytes, err := os.ReadFile(path)
		if err != nil {
			log.Warnf("peer-epm-enrolment: read %s: %v", path, err)
			continue
		}
		if err := epm.VerifyEPMSignature(epmBytes); err != nil {
			log.Warnf("peer-epm-enrolment: %s failed signature verification, skipped: %v", path, err)
			continue
		}
		peerIDStr, err := epm.PeerIDFromEPM(epmBytes)
		if err != nil || peerIDStr == "" {
			log.Warnf("peer-epm-enrolment: %s advertises no peer id, skipped: %v", path, err)
			continue
		}
		pid, err := peer.Decode(peerIDStr)
		if err != nil {
			log.Warnf("peer-epm-enrolment: %s advertises invalid peer id %q, skipped: %v", path, peerIDStr, err)
			continue
		}
		tp, err := registry.GetPeer(pid)
		if err != nil {
			tp = &peers.TrustedPeer{ID: pid, TrustLevel: peers.Standard}
			if addErr := registry.AddPeer(tp); addErr != nil {
				log.Warnf("peer-epm-enrolment: add peer %s: %v", peerIDStr, addErr)
				continue
			}
			tp, err = registry.GetPeer(pid)
			if err != nil {
				continue
			}
		}
		tp.EPMData = append([]byte(nil), epmBytes...)
		if vcardStr, err := vcard.EPMToVCard(epmBytes); err == nil {
			tp.VCardData = vcardStr
		}
		if err := registry.UpdatePeer(tp); err != nil {
			log.Warnf("peer-epm-enrolment: persist %s: %v", peerIDStr, err)
		}
		loaded++
		log.Infof("peer-epm-enrolment: loaded signed EPM for %s from %s (%d bytes)", peerIDStr, entry.Name(), len(epmBytes))
	}
	return loaded
}

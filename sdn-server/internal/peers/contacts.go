package peers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// ContactCard is what a contact card names: the peer, the card itself, and
// the signed profile behind it.
type ContactCard struct {
	ID      peer.ID
	VCard   string
	Profile []byte
}

// CardReader reads a contact card, a vCard's text or else a picture of a QR
// code as a data URL, and refuses one without a signed profile that names
// its peer. The node wires in the address book's reader: this package
// cannot import internal/epm.
type CardReader func(card, qr string) (ContactCard, error)

// maxContactBytes bounds a contact import: a vCard, or a QR picture sent as
// a data URL.
const maxContactBytes = 8 << 20

type contactRequest struct {
	VCard string `json:"vcard"`
	QR    string `json:"qr"`
}

// importContact enters the node or account a card names into the registry,
// so it can be filed among this node's contacts (owner 2026-10-07:
// "Contacts" for nodes and external accounts, apart from the Node Directory
// the node signs). A contact is not trust: a new entry takes the level the
// node already gives that peer, and a peer the registry holds is left as it
// is. Answers {peer_id, peer}.
func (h *APIHandler) importContact(w http.ResponseWriter, r *http.Request) {
	var req contactRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxContactBytes)).Decode(&req); err != nil {
		contactProblem(w, http.StatusBadRequest, "bad_request", "Send a vCard or a QR code.")
		return
	}
	card, qr := strings.TrimSpace(req.VCard), strings.TrimSpace(req.QR)
	if (card == "") == (qr == "") {
		contactProblem(w, http.StatusBadRequest, "bad_request", "Send one of vcard or qr.")
		return
	}
	if h.ReadCard == nil {
		contactProblem(w, http.StatusNotImplemented, "no_card_reader", "This node cannot read contact cards.")
		return
	}
	contact, err := h.ReadCard(card, qr)
	if err != nil {
		contactProblem(w, http.StatusUnprocessableEntity, "unreadable_card", err.Error())
		return
	}
	id := contact.ID
	if _, err := h.registry.GetPeer(id); err != nil {
		entry := &TrustedPeer{
			ID:         id,
			TrustLevel: h.registry.GetTrustLevel(id),
			AddedAt:    time.Now(),
			VCardData:  strings.TrimSpace(contact.VCard),
			EPMData:    contact.Profile,
		}
		if err := h.registry.AddPeer(entry); err != nil && !errors.Is(err, ErrPeerAlreadyExists) {
			contactProblem(w, http.StatusInternalServerError, "not_saved", "The contact could not be saved.")
			return
		}
	}
	held, _ := h.registry.GetPeer(id)
	writeJSONStatus(w, http.StatusCreated, map[string]any{"peer_id": id.String(), "peer": held})
}

func contactProblem(w http.ResponseWriter, status int, code, message string) {
	writeJSONStatus(w, status, map[string]string{"code": code, "message": message})
}

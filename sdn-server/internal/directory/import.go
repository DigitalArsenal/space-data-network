package directory

import (
	"fmt"
	"strings"

	govcard "github.com/emersion/go-vcard"

	sdnepm "github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	sdnvcard "github.com/spacedatanetwork/sdn-server/internal/vcard"
)

const (
	vCardPropDirectoryKind  = "X-SDN-DIRECTORY-KIND"
	vCardPropPeerID         = "X-SDN-PEER-ID"
	vCardPropBitcoinAddress = "X-SDN-BITCOIN-ADDRESS"
	vCardPropBitcoinLegacy  = "X-BITCOIN-ADDRESS"
	vCardPropEPMCID         = "X-SDN-EPM-CID"
)

// ImportRecordRequest describes a manually imported directory record.
type ImportRecordRequest struct {
	Kind    string         `json:"kind"`
	Source  string         `json:"source"`
	EPMCID  string         `json:"epm_cid"`
	EPMJSON map[string]any `json:"epm_json"`
	Record  map[string]any `json:"record"`
	VCard   string         `json:"vcard"`
}

// ImportRecordResult reports the record that was inserted into the directory.
type ImportRecordResult struct {
	Imported int
	Nodes    []storage.DirectoryRecord
	Users    []storage.DirectoryRecord
}

// ImportRecord normalizes an uploaded EPM JSON or SDN vCard into the directory.
func (s *Service) ImportRecord(req ImportRecordRequest) (ImportRecordResult, error) {
	epmJSON := req.EPMJSON
	if epmJSON == nil {
		epmJSON = req.Record
	}

	epmCID := strings.TrimSpace(req.EPMCID)
	if strings.TrimSpace(req.VCard) != "" {
		if embeddedEPM, err := sdnvcard.EmbeddedEPMFromVCard(req.VCard); err != nil {
			return ImportRecordResult{}, err
		} else if len(embeddedEPM) > 0 {
			if err := sdnepm.VerifyEPMSignature(embeddedEPM); err != nil {
				return ImportRecordResult{}, fmt.Errorf("verify embedded EPM signature: %w", err)
			}
			record, err := sdnepm.DirectoryRecordJSONFromEPM(embeddedEPM, "")
			if err != nil {
				return ImportRecordResult{}, err
			}
			epmJSON = record
			if computedCID, err := sdnepm.ComputeEPMCID(embeddedEPM); err != nil {
				return ImportRecordResult{}, err
			} else {
				epmCID = computedCID
			}
		} else {
			record, vcardCID, err := directoryRecordFromVCard(req.VCard)
			if err != nil {
				return ImportRecordResult{}, err
			}
			epmJSON = record
			if epmCID == "" {
				epmCID = vcardCID
			}
		}
	}
	if epmJSON == nil {
		return ImportRecordResult{}, fmt.Errorf("epm_json, record, or vcard is required")
	}

	kind := strings.TrimSpace(strings.ToLower(req.Kind))
	if kind == "" {
		kind = InferKind(epmJSON)
	}

	source := strings.TrimSpace(req.Source)
	if source == "" {
		source = "manual-upload"
	}

	record, err := s.importEPMJSON(kind, epmJSON, epmCID, source)
	if err != nil {
		return ImportRecordResult{}, err
	}

	result := ImportRecordResult{Imported: 1}
	if record.Kind == KindNode {
		result.Nodes = []storage.DirectoryRecord{record}
	} else {
		result.Users = []storage.DirectoryRecord{record}
	}
	return result, nil
}

func (s *Service) importEPMJSON(kind string, epmJSON map[string]any, epmCID, source string) (storage.DirectoryRecord, error) {
	if s == nil || s.store == nil {
		return storage.DirectoryRecord{}, fmt.Errorf("directory store is not configured")
	}
	record, err := normalizeRecord(kind, epmJSON, epmCID, source)
	if err != nil {
		return storage.DirectoryRecord{}, err
	}
	if err := s.store.UpsertDirectoryRecord(record); err != nil {
		return storage.DirectoryRecord{}, err
	}
	return record, nil
}

func directoryRecordFromVCard(vcardData string) (map[string]any, string, error) {
	dec := govcard.NewDecoder(strings.NewReader(vcardData))
	card, err := dec.Decode()
	if err != nil {
		return nil, "", fmt.Errorf("parse vcard: %w", err)
	}

	record := map[string]any{}
	if kind := vcardField(card, vCardPropDirectoryKind); kind != "" {
		record["directory_kind"] = strings.ToLower(kind)
	}
	if peerID := firstNonEmpty(
		vcardField(card, vCardPropPeerID),
		vcardField(card, govcard.FieldUID),
	); peerID != "" {
		record["peer_id"] = peerID
	}
	if dn := vcardField(card, govcard.FieldFormattedName); dn != "" {
		record["dn"] = dn
	}
	if legalName := vcardField(card, govcard.FieldOrganization); legalName != "" {
		record["legal_name"] = legalName
	}
	if bitcoinAddress := firstNonEmpty(
		vcardField(card, vCardPropBitcoinAddress),
		vcardField(card, vCardPropBitcoinLegacy),
	); bitcoinAddress != "" {
		record["bitcoin_address"] = bitcoinAddress
	}

	return record, vcardField(card, vCardPropEPMCID), nil
}

func vcardField(card govcard.Card, name string) string {
	field := card.Get(name)
	if field == nil {
		return ""
	}
	return strings.TrimSpace(field.Value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

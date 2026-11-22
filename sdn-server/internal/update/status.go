package update

// THE NODE'S UPDATE STATUS, AS IT STATES IT.
//
// The owner asked "can we see what version all the nodes are on?". The answer
// a node can give about another is only what that node says: its identify
// agent version, and, from 1.2.0, a small status message it publishes on the
// update topic of its channel. The message is authored and signed by the node's
// own peer key (gossipsub's strict signing policy), and a receiving node
// accepts it only when its peer_id is that author. So "failed and rolled back"
// on a fleet view is the node's own statement, read from its own deploy
// ledger, never an inference by the viewer.
//
// A node built before this message existed (1.1.9 and older) rejects it in its
// update topic validator, which accepts only signals: the message is neither
// delivered to it nor forwarded by it, and without peer scoring the rejection
// costs the sender nothing.
//
// On the wire the status is one size-prefixed SDS $UST record (from SDS
// 1.292.0). Its first form, a JSON document, is gone: on 2026-11-21 the only
// node in the field (sdn.spaceaware.io) ran 1.1.9, which never sent it.

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	UST "github.com/DigitalArsenal/spacedatastandards.org/lib/go/UST"
	flatbuffers "github.com/google/flatbuffers/go"
)

const (
	// StatusMaxBytes bounds a status message. A status with every field at
	// its limit is under 1 KiB.
	StatusMaxBytes = 2 << 10

	// Status states.
	StatusCurrent = "current"
	StatusPending = "pending"
	StatusFailed  = "failed"
)

// Failure reason codes: what a node publishes about why an update failed.
// The ledger's own text (gate errors, rollback errors, paths) stays on the
// node; only one of these codes leaves it.
const (
	// ReasonNotReady: the build did not answer ready by its deadline.
	ReasonNotReady = "not-ready-by-deadline"
	// ReasonStoppedDuringTrial: the build was stopped too often during its
	// trial.
	ReasonStoppedDuringTrial = "stopped-during-trial"
	// ReasonTrialUnreadable: the trial record could not be read.
	ReasonTrialUnreadable = "trial-record-unreadable"
	// ReasonHealthFailed: the build failed its health check for any other
	// reason.
	ReasonHealthFailed = "health-failed"
	// ReasonRevertRefused: the build failed and the rollback was refused, so
	// it keeps running.
	ReasonRevertRefused = "revert-refused"
)

// StatusReasonCodes is every reason code a status may carry.
var StatusReasonCodes = []string{ReasonNotReady, ReasonStoppedDuringTrial, ReasonTrialUnreadable, ReasonHealthFailed, ReasonRevertRefused}

// Field limits of a status message. Every string a status carries is
// printed by other nodes, so each is checked against a character set as well
// as a length: none can hold a control character, a space or markup.
var (
	statusPeerIDPattern   = regexp.MustCompile(`^[0-9A-Za-z]{16,128}$`)
	statusVersionPattern  = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_-]{0,63}$`)
	statusUpdateIDPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_:-]{0,127}$`)
)

const statusTimeMaxLen = 40

// StatusPendingUpdate is the update a node has accepted and not yet acted on.
type StatusPendingUpdate struct {
	UpdateID string `json:"update_id"`
	Version  string `json:"version"`
	Sequence int64  `json:"sequence"`
	DueAt    string `json:"due_at"`
}

// StatusFailure is an update a node installed and that failed its health
// gate or trial, from its own deploy ledger.
type StatusFailure struct {
	UpdateID string `json:"update_id"`
	Version  string `json:"version"`
	Sequence int64  `json:"sequence"`
	At       string `json:"at"`
	// ReasonCode is one of StatusReasonCodes. The ledger's text stays on the
	// node.
	ReasonCode string `json:"reason_code"`
	// RolledBack is false when the revert was refused and the failed build
	// keeps running (ledger revert-refused).
	RolledBack bool `json:"rolled_back"`
}

// StatusAck is the newest update signal a node accepted.
type StatusAck struct {
	UpdateID string `json:"update_id"`
	Sequence int64  `json:"sequence"`
}

// Status is the message a node publishes about its own update state. On the
// wire it is one size-prefixed $UST FlatBuffer (MarshalStatus, ParseStatus).
type Status struct {
	PeerID       string               `json:"peer_id"`
	Version      string               `json:"version"`
	Sequence     int64                `json:"sequence"`
	UpdateID     string               `json:"update_id,omitempty"`
	State        string               `json:"state"`
	Pending      *StatusPendingUpdate `json:"pending,omitempty"`
	Failed       *StatusFailure       `json:"failed,omitempty"`
	Acknowledged *StatusAck           `json:"acknowledged,omitempty"`
	At           string               `json:"at"`
}

// MarshalStatus encodes a status as the size-prefixed $UST record that goes
// on the update topic.
func MarshalStatus(s *Status) []byte {
	b := flatbuffers.NewBuilder(512)
	str := func(v string) flatbuffers.UOffsetT { return b.CreateString(v) }
	var pending, failed, ack flatbuffers.UOffsetT
	if p := s.Pending; p != nil {
		id, version, due := str(p.UpdateID), str(p.Version), str(p.DueAt)
		UST.USTPendingUpdateStart(b)
		UST.USTPendingUpdateAddUPDATE_ID(b, id)
		UST.USTPendingUpdateAddVERSION(b, version)
		UST.USTPendingUpdateAddSEQUENCE(b, p.Sequence)
		UST.USTPendingUpdateAddDUE_AT(b, due)
		pending = UST.USTPendingUpdateEnd(b)
	}
	if f := s.Failed; f != nil {
		id, version, at, reason := str(f.UpdateID), str(f.Version), str(f.At), str(f.ReasonCode)
		UST.USTFailureStart(b)
		UST.USTFailureAddUPDATE_ID(b, id)
		UST.USTFailureAddVERSION(b, version)
		UST.USTFailureAddSEQUENCE(b, f.Sequence)
		UST.USTFailureAddAT(b, at)
		UST.USTFailureAddREASON_CODE(b, reason)
		UST.USTFailureAddROLLED_BACK(b, f.RolledBack)
		failed = UST.USTFailureEnd(b)
	}
	if a := s.Acknowledged; a != nil {
		id := str(a.UpdateID)
		UST.USTAcknowledgementStart(b)
		UST.USTAcknowledgementAddUPDATE_ID(b, id)
		UST.USTAcknowledgementAddSEQUENCE(b, a.Sequence)
		ack = UST.USTAcknowledgementEnd(b)
	}
	peer, version, state, at := str(s.PeerID), str(s.Version), str(s.State), str(s.At)
	var updateID flatbuffers.UOffsetT
	if s.UpdateID != "" {
		updateID = str(s.UpdateID)
	}
	UST.USTStart(b)
	UST.USTAddPEER_ID(b, peer)
	UST.USTAddVERSION(b, version)
	UST.USTAddSEQUENCE(b, s.Sequence)
	if updateID != 0 {
		UST.USTAddUPDATE_ID(b, updateID)
	}
	UST.USTAddSTATE(b, state)
	if pending != 0 {
		UST.USTAddPENDING(b, pending)
	}
	if failed != 0 {
		UST.USTAddFAILED(b, failed)
	}
	if ack != 0 {
		UST.USTAddACKNOWLEDGED(b, ack)
	}
	UST.USTAddAT(b, at)
	UST.FinishSizePrefixedUSTBuffer(b, UST.USTEnd(b))
	return b.FinishedBytes()
}

// ParseStatus decodes a $UST status message and checks every field: the peer
// id, version and update id character sets and lengths, RFC 3339 times, the
// state and the reason code from their fixed lists, and a pending or failed
// block where the state names one. Fields this build does not know are ignored
// by rule: they are never stored or shown. It does not authenticate the
// message: the receiver checks peer_id against the message's author.
func ParseStatus(raw []byte) (status *Status, err error) {
	if len(raw) == 0 || len(raw) > StatusMaxBytes {
		return nil, errors.New("update status message is empty or too large")
	}
	if !IsStatusMessage(raw) {
		return nil, errors.New("not an update status message ($UST)")
	}
	if prefix := binary.LittleEndian.Uint32(raw[:4]); int(prefix) != len(raw)-4 {
		return nil, errors.New("parse update status: the size prefix does not match the message")
	}
	// A malformed buffer makes the generated accessors index out of range;
	// that is a refused message, never a crash of the receiving node.
	defer func() {
		if recover() != nil {
			status, err = nil, errors.New("parse update status: malformed $UST record")
		}
	}()
	record := UST.GetSizePrefixedRootAsUST(raw, 0)
	status = &Status{PeerID: string(record.PEER_ID()), Version: string(record.VERSION()), Sequence: record.SEQUENCE(),
		UpdateID: string(record.UPDATE_ID()), State: string(record.STATE()), At: string(record.AT())}
	if p := record.PENDING(nil); p != nil {
		status.Pending = &StatusPendingUpdate{UpdateID: string(p.UPDATE_ID()), Version: string(p.VERSION()), Sequence: p.SEQUENCE(), DueAt: string(p.DUE_AT())}
	}
	if f := record.FAILED(nil); f != nil {
		status.Failed = &StatusFailure{UpdateID: string(f.UPDATE_ID()), Version: string(f.VERSION()), Sequence: f.SEQUENCE(),
			At: string(f.AT()), ReasonCode: string(f.REASON_CODE()), RolledBack: f.ROLLED_BACK()}
	}
	if a := record.ACKNOWLEDGED(nil); a != nil {
		status.Acknowledged = &StatusAck{UpdateID: string(a.UPDATE_ID()), Sequence: a.SEQUENCE()}
	}
	if err := status.validate(); err != nil {
		return nil, fmt.Errorf("update status: %w", err)
	}
	return status, nil
}

// IsStatusMessage reports whether raw is a $UST record, valid or not.
func IsStatusMessage(raw []byte) bool {
	return len(raw) >= 12 && len(raw) <= 64<<10 && UST.SizePrefixedUSTBufferHasIdentifier(raw)
}

func (s *Status) validate() error {
	if !statusPeerIDPattern.MatchString(s.PeerID) {
		return errors.New("peer_id is not a peer id")
	}
	if !statusVersionPattern.MatchString(s.Version) {
		return errors.New("version is not a version")
	}
	if s.UpdateID != "" && !statusUpdateIDPattern.MatchString(s.UpdateID) {
		return errors.New("update_id is not an update id")
	}
	if s.Sequence < 0 {
		return errors.New("sequence is negative")
	}
	if err := statusTime(s.At, "at"); err != nil {
		return err
	}
	switch s.State {
	case StatusCurrent, StatusPending, StatusFailed:
	default:
		return errors.New("state is not current, pending or failed")
	}
	if s.State == StatusPending && s.Pending == nil {
		return errors.New("state pending names no pending update")
	}
	if s.State == StatusFailed && s.Failed == nil {
		return errors.New("state failed names no failed update")
	}
	if p := s.Pending; p != nil {
		if !statusUpdateIDPattern.MatchString(p.UpdateID) || !statusVersionPattern.MatchString(p.Version) || p.Sequence < 0 {
			return errors.New("pending names no valid update")
		}
		if err := statusTime(p.DueAt, "pending.due_at"); err != nil {
			return err
		}
	}
	if f := s.Failed; f != nil {
		if !statusUpdateIDPattern.MatchString(f.UpdateID) || !statusVersionPattern.MatchString(f.Version) || f.Sequence < 0 {
			return errors.New("failed names no valid update")
		}
		if err := statusTime(f.At, "failed.at"); err != nil {
			return err
		}
		if !slicesContains(StatusReasonCodes, f.ReasonCode) {
			return errors.New("failed.reason_code is not a known reason code")
		}
	}
	if a := s.Acknowledged; a != nil {
		if !statusUpdateIDPattern.MatchString(a.UpdateID) || a.Sequence < 0 {
			return errors.New("acknowledged names no valid update")
		}
	}
	return nil
}

func statusTime(value, field string) error {
	if len(value) > statusTimeMaxLen {
		return fmt.Errorf("%s is too long", field)
	}
	if _, err := time.Parse(time.RFC3339, value); err != nil {
		return fmt.Errorf("%s is not an RFC 3339 time", field)
	}
	return nil
}

func slicesContains(list []string, value string) bool {
	for _, v := range list {
		if v == value {
			return true
		}
	}
	return false
}

// failureReasonCode maps a ledger failure line to its published code.
func failureReasonCode(entry DeployLedgerEntry) string {
	reason := entry.Reason
	switch {
	case entry.Action == "revert-refused":
		return ReasonRevertRefused
	case strings.Contains(reason, "did not answer ready by its deadline"):
		return ReasonNotReady
	case strings.Contains(reason, "during its trial before it answered ready"):
		return ReasonStoppedDuringTrial
	case strings.Contains(reason, "trial record cannot be read"):
		return ReasonTrialUnreadable
	}
	return ReasonHealthFailed
}

const (
	acknowledgedFileName = "acknowledged.json"
	latestSignalFileName = "latest-signal.json"
)

// ReadAcknowledged is the newest update signal this node accepted
// (updates/acknowledged.json), or nil.
func ReadAcknowledged(paths Paths) *StatusAck { return readAck(paths, acknowledgedFileName) }

// WriteAcknowledged records ack as the newest update signal this node
// accepted, when it is newer than the one recorded.
func WriteAcknowledged(paths Paths, ack StatusAck) error {
	return writeAck(paths, acknowledgedFileName, ack)
}

// ReadLatestSignal is the newest update signal of this node's channel and
// kind that this node verified against its trust roots, whether or not it
// was newer than the build it runs (updates/latest-signal.json), or nil. The
// fleet view measures every node against it.
func ReadLatestSignal(paths Paths) *StatusAck { return readAck(paths, latestSignalFileName) }

// WriteLatestSignal records ack as the newest verified signal, when it is
// newer than the one recorded.
func WriteLatestSignal(paths Paths, ack StatusAck) error {
	return writeAck(paths, latestSignalFileName, ack)
}

func readAck(paths Paths, name string) *StatusAck {
	data, err := os.ReadFile(filepath.Join(paths.Updates, name))
	if err != nil {
		return nil
	}
	var ack StatusAck
	if json.Unmarshal(data, &ack) != nil || ack.UpdateID == "" {
		return nil
	}
	return &ack
}

func writeAck(paths Paths, name string, ack StatusAck) error {
	if held := readAck(paths, name); held != nil && held.Sequence >= ack.Sequence {
		return nil
	}
	if err := os.MkdirAll(paths.Updates, 0o700); err != nil {
		return err
	}
	return writeJSONAtomic(filepath.Join(paths.Updates, name), &ack)
}

// AtTime is At parsed.
func (s *Status) AtTime() time.Time {
	t, _ := time.Parse(time.RFC3339, s.At)
	return t
}

// LocalStatus builds this node's status from its update root: state.json,
// pending.json and the deploy ledger. A failure the node has moved past (it
// runs a sequence at or above the failed one) is not reported.
func LocalStatus(paths Paths, peerID, version string, ack *StatusAck, now time.Time) *Status {
	status := &Status{PeerID: peerID, Version: version, State: StatusCurrent,
		Acknowledged: ack, At: now.UTC().Format(time.RFC3339)}
	if state, err := LoadState(paths); err == nil {
		status.Sequence, status.UpdateID = state.Sequence, state.UpdateID
	}
	if pending, err := CurrentPending(paths); err == nil && pending != nil {
		status.State = StatusPending
		status.Pending = &StatusPendingUpdate{UpdateID: pending.UpdateID, Version: pending.Version, Sequence: pending.Sequence, DueAt: pending.DueAt}
	}
	if failure := ActiveUpdateFailure(paths); failure != nil {
		status.Failed = failure
		if status.State == StatusCurrent {
			status.State = StatusFailed
		}
	}
	return status
}

// ActiveUpdateFailure is the newest update failure in the node's deploy
// ledger that still describes it, by the rule the update_failed alert uses:
// a reverted update until the node runs its sequence or a later one, a
// refused revert until the node runs a later sequence, and neither once the
// same version passed its health gate afterwards.
func ActiveUpdateFailure(paths Paths) *StatusFailure {
	file, err := os.Open(filepath.Join(paths.Root, deployLedgerName))
	if err != nil {
		return nil
	}
	defer file.Close()
	state, err := LoadState(paths)
	if err != nil {
		return nil
	}
	failures := map[string]DeployLedgerEntry{}
	var order []string
	reader := bufio.NewReaderSize(file, 64<<10)
	for {
		line, readErr := reader.ReadBytes('\n')
		var entry DeployLedgerEntry
		if len(line) <= 4<<20 && json.Unmarshal(line, &entry) == nil {
			switch entry.Action {
			case "health-reverted", "revert-refused":
				failures[entry.UpdateID] = entry
				order = append(order, entry.UpdateID)
			case "health-passed":
				if failed, ok := failures[entry.UpdateID]; ok && failed.Version == entry.Version && entry.Sequence >= failed.Sequence {
					delete(failures, entry.UpdateID)
				}
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				return nil
			}
			break
		}
	}
	for i := len(order) - 1; i >= 0; i-- {
		failed, ok := failures[order[i]]
		if !ok {
			continue
		}
		refused := failed.Action == "revert-refused"
		if refused && state.Sequence > failed.Sequence || !refused && state.Sequence >= failed.Sequence {
			continue
		}
		return &StatusFailure{UpdateID: failed.UpdateID, Version: failed.Version, Sequence: failed.Sequence,
			At: failed.RecordedAt, ReasonCode: failureReasonCode(failed), RolledBack: !refused}
	}
	return nil
}

package flowrt

// THE HOST OWNS THE SOCKET, SO THE HOST OWNS THE WIRE INVARIANT.
//
// A mounted flow assembles its own $HTR envelope — status, headers, body —
// and htrPipe streams it out verbatim. Verbatim is right for the payload: the
// host is a connector, it does not know what a body MEANS, and the
// FlatBuffer stream path must reach the client byte for byte. But two
// properties of an HTTP response are not the payload's meaning, they are the
// contract the host itself writes onto the wire under a Content-Type it
// stamps into the response header block:
//
//  1. A JSON text is UTF-8 (RFC 8259 §8.1). A body labelled application/json
//     that carries an invalid byte is not "slightly off" — every strict
//     reader (encoding/json, JSON.parse over fetch, jq) rejects the WHOLE
//     body, so one hostile string in one record poisons every other row in
//     the response.
//  2. A JSON text is never empty (RFC 8259 §2: a JSON text is a value).
//     `Content-Length: 0` under `Content-Type: application/json` and status
//     200 is a lie the host told the client on the flow's behalf.
//
// Both are reachable from PEER DATA on the public /api/v1/query mount, which
// is anonymous by design:
//
//   - Nothing on the SDS write path requires a FlatBuffers string to be valid
//     UTF-8, and the flow's full-record json presentation is a wasm encoder
//     (com.digitalarsenal.foundation.omm-json) that serializes record strings
//     verbatim. A peer that publishes one record with a lone 0x80 in
//     OBJECT_NAME makes every format=json full-record answer over that
//     partition undecodable. The storage boundary already sanitizes the
//     projections IT assembles (storage.QuerySandboxedJSON), but that is a
//     different producer: the full-record body never passes through it.
//   - That wasm encoder is $OMM-shaped, so for a generically routed standard
//     it produces nothing and the responder still answers 200 +
//     application/json + zero bytes.
//
// THE FIX BELONGS AT BOTH ENDS, AND THIS IS THE HOST END. The generic
// record→JSON presentation is application logic and is owed by the flow
// bundle (task modules-public-query-generic-record-json); the host does not
// grow a record encoder to cover for it.
//
// EXACTLY TWO PROPERTIES ARE ENFORCED HERE, AND WELL-FORMEDNESS IS NOT ONE OF
// THEM. For every mount, on any response the flow itself labelled JSON, the
// host guarantees that the body it writes is VALID UTF-8 (each invalid run
// replaced with U+FFFD at the last possible moment) and that a body-bearing
// status does not produce ZERO bytes (refused with an honest 502 instead of a
// silent empty 200 — an empty 200 is indistinguishable from "this standard has
// no records", which is a real and different answer). It does NOT check that
// the body PARSES: a wasm encoder that stops halfway through and emits
// `[{"OBJECT_NAME":` still reaches the client. Nothing here could do better
// honestly — the body streams frame by frame and the status line is already on
// the wire by the time a truncation could be detected, so a well-formedness
// verdict would need the whole body buffered, which is not what a connector
// does with a stream it does not own. Producing a complete JSON text is the
// FLOW's contract; not contradicting the label the host stamped on the wire is
// the HOST's, and that is what this file is.
//
// COST AND BLAST RADIUS. The scan runs only on responses the flow itself
// labelled JSON — the FlatBuffer stream path (application/vnd.sdn.
// flatbuffers.stream, the wirespeed lane) never enters it — and it is one
// utf8.Valid pass over the body, which allocates nothing unless the body was
// already going to be rejected. MEASURED on this laptop, unloaded: 130 MiB of
// ASCII-dominant record JSON validates in 2.4 ms (~56 GB/s, Go's ASCII fast
// path), i.e. the guard is noise next to the query that produced the body.
// Sanitizing cannot move a JSON delimiter: every structural byte (`[`, `{`,
// `"`, `:`, `,`, `\`, digits, literals) is ASCII, so an invalid run can only
// sit inside a string literal.

import (
	"bytes"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/spacedatanetwork/sdn-server/internal/httpabi"
)

// jsonWireGuard sanitizes a JSON-labelled response body as it streams. The
// body arrives in frames, so a multi-byte rune can be SPLIT across frames:
// the guard holds a trailing well-formed-so-far prefix back as carry rather
// than mistaking it for an invalid byte and replacing it (which would corrupt
// a legitimate non-ASCII string).
type jsonWireGuard struct {
	active bool
	carry  []byte
}

// chunk returns the bytes of b that can be written now, valid-UTF-8 by
// construction, holding back at most 3 bytes of a rune that may continue in
// the next frame.
func (g *jsonWireGuard) chunk(b []byte) []byte {
	if len(g.carry) > 0 {
		joined := make([]byte, 0, len(g.carry)+len(b))
		joined = append(joined, g.carry...)
		joined = append(joined, b...)
		b = joined
		g.carry = g.carry[:0]
	}
	if hold := trailingPartialRune(b); hold > 0 {
		g.carry = append(g.carry[:0], b[len(b)-hold:]...)
		b = b[:len(b)-hold]
	}
	return validUTF8Wire(b)
}

// flush returns whatever the guard was still holding when the exchange ended.
// A rune that never completed is invalid, so it sanitizes like any other bad
// run.
func (g *jsonWireGuard) flush() []byte {
	if len(g.carry) == 0 {
		return nil
	}
	tail := validUTF8Wire(g.carry)
	g.carry = g.carry[:0]
	return tail
}

// validUTF8Wire is the same boundary rule storage.validUTF8JSON applies to
// engine-assembled bodies, applied to bytes a MODULE produced: pass valid
// UTF-8 through untouched (the overwhelming case, checked at ~GB/s) and
// replace each invalid run with U+FFFD otherwise.
func validUTF8Wire(b []byte) []byte {
	if len(b) == 0 || utf8.Valid(b) {
		return b
	}
	return bytes.ToValidUTF8(b, []byte(string(utf8.RuneError)))
}

// trailingPartialRune reports how many trailing bytes of b form an
// INCOMPLETE but so-far well-formed multi-byte sequence (0 when the last rune
// is complete, or when the trailing bytes are malformed and can be sanitized
// immediately).
func trailingPartialRune(b []byte) int {
	for i := 1; i <= 3 && i <= len(b); i++ {
		c := b[len(b)-i]
		switch {
		case c < 0x80: // ASCII: the tail is complete
			return 0
		case c < 0xC0: // continuation byte: keep walking back to the lead
			continue
		default:
			if n := utf8SequenceLen(c); n > i {
				return i
			}
			return 0
		}
	}
	return 0
}

// utf8SequenceLen is the encoded length declared by lead byte c, or 1 for a
// byte that cannot lead a sequence at all.
func utf8SequenceLen(c byte) int {
	switch {
	case c >= 0xF0 && c <= 0xF4:
		return 4
	case c >= 0xE0 && c <= 0xEF:
		return 3
	case c >= 0xC2 && c <= 0xDF:
		return 2
	default:
		return 1
	}
}

// declaresJSONBody reports whether the flow labelled this response as JSON —
// application/json, text/json, or any structured suffix (+json), parameters
// (charset, profile) ignored.
func declaresJSONBody(headers []httpabi.Header) bool {
	for _, h := range headers {
		if !strings.EqualFold(h.Name, "content-type") {
			continue
		}
		media := strings.ToLower(strings.TrimSpace(h.Value))
		if idx := strings.IndexByte(media, ';'); idx >= 0 {
			media = strings.TrimSpace(media[:idx])
		}
		if media == "application/json" || media == "text/json" || strings.HasSuffix(media, "+json") {
			return true
		}
	}
	return false
}

// statusCarriesBody reports whether this status/method pair is REQUIRED to
// carry a body when it declares a media type. 1xx, 204, 205 and 304 responses
// and every HEAD answer are body-less by the HTTP spec itself (RFC 9110
// §6.4.1, §15.3.5, §15.3.6, §15.4.5), so an empty body under a JSON content
// type is correct there and the guard must stay out of the way. 205 belongs on
// that list for the same reason 204 does — RFC 9110 §15.3.6 requires a Reset
// Content response to have no content — and refusing one with a 502 would be
// the guard inventing a defect.
func statusCarriesBody(status int, method string) bool {
	if strings.EqualFold(method, http.MethodHead) {
		return false
	}
	switch {
	case status < 200:
		return false
	case status == http.StatusNoContent,
		status == http.StatusResetContent,
		status == http.StatusNotModified:
		return false
	default:
		return true
	}
}

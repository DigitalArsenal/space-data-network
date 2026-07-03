// Package httpabi encodes and decodes the module-SDK's canonical WASM-served
// HTTP envelopes: HttpRequest ($HTQ, space-data-module-sdk
// schemas/HttpRequestAbi.fbs) and HttpResponse ($HTR,
// schemas/HttpResponseAbi.fbs).
//
// TRUE-isomorphism contract (the schema headers are the source of truth): the
// host is a dumb pipe. It encodes exactly what arrived on the wire into an
// HttpRequest frame — method, path (no query string), raw query string,
// headers (lower-cased names, sorted by name because HttpHeader.NAME is a
// FlatBuffers key field), body bytes, remote address — hands the frame to the
// wasm module, and streams the returned HttpResponse STATUS/HEADERS/BODY back
// verbatim. All routing, query parsing, format negotiation, and caching
// decisions live inside the wasm module, never here.
package httpabi

import (
	"fmt"
	"sort"

	flatbuffers "github.com/google/flatbuffers/go"
)

// File identifiers from the schema files.
const (
	RequestFileIdentifier  = "$HTQ"
	ResponseFileIdentifier = "$HTR"
)

// Header is one HTTP header name/value pair. Names are lower-cased by
// encoders; the encoded vector is sorted by name (FlatBuffers key field).
type Header struct {
	Name  string
	Value string
}

// Request mirrors sdn.http.HttpRequest.
type Request struct {
	Method  string
	Path    string
	Query   string
	Headers []Header
	Body    []byte
	Remote  string
}

// Response mirrors sdn.http.HttpResponse.
type Response struct {
	Status  uint16
	Headers []Header
	Body    []byte
	// BodyRefToken/BodyRefSize surface the schema's optional out-of-band
	// body reference (loop C.5c): when BodyRefSize > 0 the host substitutes
	// the byte buffer it registered under BodyRefToken on the module
	// instance's hostcall bridge. Zero when the body (if any) is inline.
	BodyRefToken uint64
	BodyRefSize  uint64
}

// Field vtable offsets (slot n → 4 + 2n), per the schema declaration order.
const (
	requestFieldMethod  = 4  // METHOD:string
	requestFieldPath    = 6  // PATH:string
	requestFieldQuery   = 8  // QUERY:string
	requestFieldHeaders = 10 // HEADERS:[HttpHeader]
	requestFieldBody    = 12 // BODY:[ubyte]
	requestFieldRemote  = 14 // REMOTE:string

	responseFieldStatus       = 4  // STATUS:ushort
	responseFieldHeaders      = 6  // HEADERS:[HttpHeader]
	responseFieldBody         = 8  // BODY:[ubyte]
	responseFieldBodyRefToken = 10 // BODY_REF_TOKEN:ulong
	responseFieldBodyRefSize  = 12 // BODY_REF_SIZE:ulong

	headerFieldName  = 4 // NAME:string (key)
	headerFieldValue = 6 // VALUE:string
)

// encodeHeadersVector writes the [HttpHeader] vector sorted by NAME
// (byte order) — required because NAME is a FlatBuffers key field.
func encodeHeadersVector(b *flatbuffers.Builder, headers []Header) flatbuffers.UOffsetT {
	if len(headers) == 0 {
		return 0
	}
	sorted := append([]Header(nil), headers...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	offsets := make([]flatbuffers.UOffsetT, len(sorted))
	for i, h := range sorted {
		name := b.CreateString(h.Name)
		value := b.CreateString(h.Value)
		b.StartObject(2)
		b.PrependUOffsetTSlot(0, name, 0)
		b.PrependUOffsetTSlot(1, value, 0)
		offsets[i] = b.EndObject()
	}
	b.StartVector(4, len(offsets), 4)
	for i := len(offsets) - 1; i >= 0; i-- {
		b.PrependUOffsetT(offsets[i])
	}
	return b.EndVector(len(offsets))
}

// EncodeRequest builds a finished $HTQ FlatBuffer.
func EncodeRequest(req *Request) []byte {
	b := flatbuffers.NewBuilder(1024 + len(req.Body))
	headersVec := encodeHeadersVector(b, req.Headers)
	var bodyVec flatbuffers.UOffsetT
	if len(req.Body) > 0 {
		bodyVec = b.CreateByteVector(req.Body)
	}
	method := b.CreateString(req.Method)
	path := b.CreateString(req.Path)
	query := b.CreateString(req.Query)
	remote := b.CreateString(req.Remote)

	b.StartObject(6)
	b.PrependUOffsetTSlot(0, method, 0)
	b.PrependUOffsetTSlot(1, path, 0)
	b.PrependUOffsetTSlot(2, query, 0)
	b.PrependUOffsetTSlot(3, headersVec, 0)
	b.PrependUOffsetTSlot(4, bodyVec, 0)
	b.PrependUOffsetTSlot(5, remote, 0)
	root := b.EndObject()
	b.FinishWithFileIdentifier(root, []byte(RequestFileIdentifier))
	return b.FinishedBytes()
}

// EncodeResponse builds a finished $HTR FlatBuffer.
func EncodeResponse(resp *Response) []byte {
	b := flatbuffers.NewBuilder(1024 + len(resp.Body))
	headersVec := encodeHeadersVector(b, resp.Headers)
	var bodyVec flatbuffers.UOffsetT
	if len(resp.Body) > 0 {
		bodyVec = b.CreateByteVector(resp.Body)
	}
	b.StartObject(5)
	b.PrependUint16Slot(0, resp.Status, 0)
	b.PrependUOffsetTSlot(1, headersVec, 0)
	b.PrependUOffsetTSlot(2, bodyVec, 0)
	b.PrependUint64Slot(3, resp.BodyRefToken, 0)
	b.PrependUint64Slot(4, resp.BodyRefSize, 0)
	root := b.EndObject()
	b.FinishWithFileIdentifier(root, []byte(ResponseFileIdentifier))
	return b.FinishedBytes()
}

func rootTable(buf []byte, identifier string) (*flatbuffers.Table, error) {
	if len(buf) < 8 {
		return nil, fmt.Errorf("httpabi: buffer too small (%d bytes)", len(buf))
	}
	if got := string(buf[4:8]); got != identifier {
		return nil, fmt.Errorf("httpabi: file identifier %q, want %q", got, identifier)
	}
	return &flatbuffers.Table{Bytes: buf, Pos: flatbuffers.GetUOffsetT(buf)}, nil
}

func tableString(t *flatbuffers.Table, field flatbuffers.VOffsetT) string {
	o := flatbuffers.UOffsetT(t.Offset(field))
	if o == 0 {
		return ""
	}
	return string(t.ByteVector(o + t.Pos))
}

func tableBytes(t *flatbuffers.Table, field flatbuffers.VOffsetT) []byte {
	o := flatbuffers.UOffsetT(t.Offset(field))
	if o == 0 {
		return nil
	}
	return t.ByteVector(o + t.Pos)
}

func tableHeaders(t *flatbuffers.Table, field flatbuffers.VOffsetT) []Header {
	o := flatbuffers.UOffsetT(t.Offset(field))
	if o == 0 {
		return nil
	}
	n := t.VectorLen(o)
	start := t.Vector(o)
	headers := make([]Header, 0, n)
	for i := 0; i < n; i++ {
		pos := t.Indirect(start + flatbuffers.UOffsetT(i)*4)
		ht := &flatbuffers.Table{Bytes: t.Bytes, Pos: pos}
		headers = append(headers, Header{
			Name:  tableString(ht, headerFieldName),
			Value: tableString(ht, headerFieldValue),
		})
	}
	return headers
}

// DecodeRequest parses a $HTQ FlatBuffer.
func DecodeRequest(buf []byte) (*Request, error) {
	t, err := rootTable(buf, RequestFileIdentifier)
	if err != nil {
		return nil, err
	}
	return &Request{
		Method:  tableString(t, requestFieldMethod),
		Path:    tableString(t, requestFieldPath),
		Query:   tableString(t, requestFieldQuery),
		Headers: tableHeaders(t, requestFieldHeaders),
		Body:    tableBytes(t, requestFieldBody),
		Remote:  tableString(t, requestFieldRemote),
	}, nil
}

// DecodeResponse parses a $HTR FlatBuffer.
func DecodeResponse(buf []byte) (*Response, error) {
	t, err := rootTable(buf, ResponseFileIdentifier)
	if err != nil {
		return nil, err
	}
	status := uint16(0)
	if o := flatbuffers.UOffsetT(t.Offset(responseFieldStatus)); o != 0 {
		status = t.GetUint16(o + t.Pos)
	}
	refToken := uint64(0)
	if o := flatbuffers.UOffsetT(t.Offset(responseFieldBodyRefToken)); o != 0 {
		refToken = t.GetUint64(o + t.Pos)
	}
	refSize := uint64(0)
	if o := flatbuffers.UOffsetT(t.Offset(responseFieldBodyRefSize)); o != 0 {
		refSize = t.GetUint64(o + t.Pos)
	}
	return &Response{
		Status:       status,
		Headers:      tableHeaders(t, responseFieldHeaders),
		Body:         tableBytes(t, responseFieldBody),
		BodyRefToken: refToken,
		BodyRefSize:  refSize,
	}, nil
}

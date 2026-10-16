package modulert

// Per-operation translation between a typed hostcall (HostOps records + raw / list frames) and the request map /
// answer of the capability handler that serves it. Operations without an entry use defaultTypedRequest /
// defaultTypedAnswer (lower-cased record fields in, upper-cased answer keys out).

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/modulert/hostops"
)

type typedAdapter struct {
	in  func(*typedCall) (map[string]any, error)
	out func(*typedCall, any, [][]byte) (*typedAnswer, error)
}

// cryptoAlgorithmNames maps the HostOps cryptoAlgorithm enum to the node handlers' algorithm names.
var cryptoAlgorithmNames = map[string]string{
	"Default": "", "Ed25519": "ed25519", "Secp256k1": "secp256k1", "X25519": "x25519",
	"Sha256": "sha256", "Sha512": "sha512", "Aes256Gcm": "aes-256-gcm", "HkdfSha256": "hkdf-sha256",
}

func enumName(enum string, v any) string {
	values := hostops.Enums[enum]
	i := int(num(v))
	if i < 0 || i >= len(values) {
		return ""
	}
	return values[i]
}

func cryptoAlgorithm(call *typedCall) (string, error) {
	name := enumName("cryptoAlgorithm", call.params["ALGORITHM"])
	algorithm, ok := cryptoAlgorithmNames[name]
	if !ok {
		return "", typedErr("unsupported", "%s: algorithm %s is not served by this host", call.op, name)
	}
	return algorithm, nil
}

// rawB64 sets key to the base64 of a raw port when the module sent it.
func rawB64(request map[string]any, key string, call *typedCall, port string) {
	if data, ok := call.raw[port]; ok {
		request[key] = b64(data)
	}
}

func firstRaw(call *typedCall, ports ...string) ([]byte, bool) {
	for _, p := range ports {
		if data, ok := call.raw[p]; ok {
			return data, true
		}
	}
	return nil, false
}

func dataAnswer(port string) func(*typedCall, any, [][]byte) (*typedAnswer, error) {
	return func(call *typedCall, result any, segments [][]byte) (*typedAnswer, error) {
		data, ok := bytesOf(result, segments)
		if !ok {
			return nil, typedErr("failed", "%s: the host answered no bytes", call.op)
		}
		return &typedAnswer{raw: map[string][]byte{port: data}}, nil
	}
}

func fieldBytes(field, port string) func(*typedCall, any, [][]byte) (*typedAnswer, error) {
	return func(call *typedCall, result any, segments [][]byte) (*typedAnswer, error) {
		object, _ := result.(map[string]any)
		data, ok := bytesOf(object[field], segments)
		if !ok {
			return nil, typedErr("failed", "%s: the host answered no %s", call.op, field)
		}
		return &typedAnswer{raw: map[string][]byte{port: data}}, nil
	}
}

func boolAnswer(field string) func(*typedCall, any, [][]byte) (*typedAnswer, error) {
	return func(call *typedCall, result any, _ [][]byte) (*typedAnswer, error) {
		if b, ok := result.(bool); ok {
			return &typedAnswer{result: map[string]any{"VALUE": b}}, nil
		}
		object, _ := result.(map[string]any)
		b, _ := object[field].(bool)
		return &typedAnswer{result: map[string]any{"VALUE": b}}, nil
	}
}

func noAnswer(*typedCall, any, [][]byte) (*typedAnswer, error) { return &typedAnswer{}, nil }

func withAlgorithm(build func(call *typedCall, request map[string]any)) func(*typedCall) (map[string]any, error) {
	return func(call *typedCall) (map[string]any, error) {
		algorithm, err := cryptoAlgorithm(call)
		if err != nil {
			return nil, err
		}
		request := map[string]any{}
		if algorithm != "" {
			request["algorithm"] = algorithm
		}
		build(call, request)
		return request, nil
	}
}

func lengthOf(call *typedCall, def int) int {
	if n := int(num(call.params["LENGTH"])); n > 0 {
		return n
	}
	return def
}

// headerBlock: "name: value" lines <-> a header map.
func headersFromBlock(block []byte) map[string]string {
	headers := map[string]string{}
	for _, line := range strings.Split(strings.ReplaceAll(string(block), "\r\n", "\n"), "\n") {
		if at := strings.Index(line, ":"); at > 0 {
			headers[strings.TrimSpace(line[:at])] = strings.TrimSpace(line[at+1:])
		}
	}
	return headers
}

func headerBlock(object map[string]any) []byte {
	var lines []string
	if values, ok := object["header_values"].(map[string]any); ok {
		for _, name := range sortedKeys(values) {
			if list, ok := values[name].([]any); ok {
				for _, v := range list {
					lines = append(lines, http.CanonicalHeaderKey(name)+": "+str(v))
				}
			}
		}
	} else if values, ok := object["headers"].(map[string]any); ok {
		for _, name := range sortedKeys(values) {
			lines = append(lines, http.CanonicalHeaderKey(name)+": "+str(values[name]))
		}
	}
	return []byte(strings.Join(lines, "\r\n"))
}

// SQL values travel in FlatSQL's own tagged parameter encoding (raw "params" in, raw "cells" out): value after
// value, a u8 tag, a u32 little-endian payload length, the payload.
const (
	sqlTagNull  = 0
	sqlTagBool  = 1
	sqlTagI64   = 2
	sqlTagF64   = 3
	sqlTagStr   = 4
	sqlTagBytes = 5
)

var sqlTagNames = []string{"null", "bool", "i64", "f64", "str", "bytes"}

// sqlFixedSize is the payload size of the fixed-width tags (-1: any length).
var sqlFixedSize = []int{0, 1, 8, 8, -1, -1}

// decodeSQLValues splits a tagged SQL value run into its tags and payloads (the payloads alias data).
func decodeSQLValues(data []byte) (tags []byte, payloads [][]byte, err error) {
	for at := 0; at < len(data); {
		if len(data)-at < 5 {
			return nil, nil, fmt.Errorf("truncated value header")
		}
		tag, size := data[at], binary.LittleEndian.Uint32(data[at+1:])
		start := at + 5
		if uint64(len(data)-start) < uint64(size) {
			return nil, nil, fmt.Errorf("payload exceeds the buffer")
		}
		if int(tag) >= len(sqlTagNames) {
			return nil, nil, fmt.Errorf("unknown tag %d", tag)
		}
		if fixed := sqlFixedSize[tag]; fixed >= 0 && int(size) != fixed {
			return nil, nil, fmt.Errorf("a %s carries %d bytes, not %d", sqlTagNames[tag], fixed, size)
		}
		tags = append(tags, tag)
		payloads = append(payloads, data[start:start+int(size)])
		at = start + int(size)
	}
	return tags, payloads, nil
}

// appendSQLValue appends one tagged SQL value.
func appendSQLValue(out []byte, tag byte, payload []byte) []byte {
	var hdr [5]byte
	hdr[0] = tag
	binary.LittleEndian.PutUint32(hdr[1:], uint32(len(payload)))
	return append(append(out, hdr[:]...), payload...)
}

// maxExactInteger is 2^53: an i64 beyond it travels to the handlers as text so it arrives whole.
const maxExactInteger = 1 << 53

// sqlParams: raw "params" -> the handlers' tagged params ({"t","v"}; bytes as base64). nil when the module sent none.
func sqlParams(call *typedCall) ([]any, error) {
	data, ok := call.raw["params"]
	if !ok {
		return nil, nil
	}
	tags, payloads, err := decodeSQLValues(data)
	if err != nil {
		return nil, typedErr("invalid-params", "%s: params: %v", call.op, err)
	}
	out := make([]any, 0, len(tags))
	for i, tag := range tags {
		payload := payloads[i]
		switch tag {
		case sqlTagNull:
			out = append(out, map[string]any{"t": "null"})
		case sqlTagBool:
			out = append(out, map[string]any{"t": "bool", "v": payload[0] != 0})
		case sqlTagI64:
			v := int64(binary.LittleEndian.Uint64(payload))
			if v > maxExactInteger || v < -maxExactInteger {
				out = append(out, map[string]any{"t": "i64", "v": strconv.FormatInt(v, 10)})
			} else {
				out = append(out, map[string]any{"t": "i64", "v": v})
			}
		case sqlTagF64:
			v := math.Float64frombits(binary.LittleEndian.Uint64(payload))
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return nil, typedErr("invalid-params", "%s: params: value %d is %v (this host takes finite f64)", call.op, i, v)
			}
			out = append(out, map[string]any{"t": "f64", "v": v})
		case sqlTagStr:
			out = append(out, map[string]any{"t": "str", "v": string(payload)})
		case sqlTagBytes:
			out = append(out, map[string]any{"t": "bytes", "v": b64(payload)})
		}
	}
	return out, nil
}

// deliverRef: DELIVER Ref is "ref" for the handlers; Inline is left out (the handlers' default).
func deliverRef(call *typedCall) string {
	if enumName("deliverMode", call.params["DELIVER"]) == "Ref" {
		return "ref"
	}
	return ""
}

// withDeliver replaces the record's numeric "deliver" with the handlers' "ref".
func withDeliver(request map[string]any, call *typedCall) map[string]any {
	delete(request, "deliver")
	if d := deliverRef(call); d != "" {
		request["deliver"] = d
	}
	return request
}

func sqlRequest(call *typedCall) (map[string]any, error) {
	request := map[string]any{"sql": string(call.raw["sql"])}
	params, err := sqlParams(call)
	if err != nil {
		return nil, err
	}
	if params != nil {
		request["params"] = params
	}
	return withDeliver(request, call), nil
}

// streamAnswer: {rows, columns, stream: $bin | ref: {token,size,fnv1a64,frames}} -> StreamResult-shaped record + raw.
func streamAnswer(call *typedCall, result any, segments [][]byte) (*typedAnswer, error) {
	object, _ := result.(map[string]any)
	record := map[string]any{"ROWS": integer(object["rows"]), "COLUMNS": integer(object["columns"])}
	answer := &typedAnswer{result: record, raw: map[string][]byte{}}
	if ref, ok := object["ref"].(map[string]any); ok {
		record["REF"] = bodyRef(ref)
	} else if data, ok := bytesOf(object["stream"], segments); ok {
		answer.raw["stream"] = data
	}
	return answer, nil
}

func sizePrefixed(chunks [][]byte) []byte {
	var out []byte
	var hdr [4]byte
	for _, c := range chunks {
		binary.LittleEndian.PutUint32(hdr[:], uint32(len(c)))
		out = append(out, hdr[:]...)
		out = append(out, c...)
	}
	return out
}

func unixMs(v any) int64 {
	if t, err := time.Parse(time.RFC3339Nano, str(v)); err == nil {
		return t.UnixMilli()
	}
	return 0
}

// orderedRows reads a JSON array of row objects keeping each object's key order (the engine writes columns in
// projection order): the column names and the row-major cells.
func orderedRows(doc []byte) ([]string, [][]any, error) {
	decoder := json.NewDecoder(strings.NewReader(string(doc)))
	decoder.UseNumber()
	if tok, err := decoder.Token(); err != nil || tok != json.Delim('[') {
		return nil, nil, fmt.Errorf("rows are not a JSON array")
	}
	var columns []string
	var rows [][]any
	for decoder.More() {
		if tok, err := decoder.Token(); err != nil || tok != json.Delim('{') {
			return nil, nil, fmt.Errorf("a row is not an object")
		}
		var row []any
		col := 0
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return nil, nil, err
			}
			var value any
			if err := decoder.Decode(&value); err != nil {
				return nil, nil, err
			}
			if len(rows) == 0 {
				columns = append(columns, str(key))
			}
			row = append(row, value)
			col++
		}
		decoder.Token() //nolint:errcheck // '}'
		rows = append(rows, row)
	}
	return columns, rows, nil
}

// appendSQLCell appends a rows-answer cell (a JSON value) as a tagged SQL value: an integer is i64, any other number
// f64 (as the SDK's JS hosts tag it: 1.0 is an integer there too).
func appendSQLCell(out []byte, v any) ([]byte, error) {
	var word [8]byte
	switch t := v.(type) {
	case nil:
		return appendSQLValue(out, sqlTagNull, nil), nil
	case bool:
		b := byte(0)
		if t {
			b = 1
		}
		return appendSQLValue(out, sqlTagBool, []byte{b}), nil
	case json.Number:
		if i, err := t.Int64(); err == nil {
			binary.LittleEndian.PutUint64(word[:], uint64(i))
			return appendSQLValue(out, sqlTagI64, word[:]), nil
		}
		f, err := t.Float64()
		if err != nil {
			return nil, fmt.Errorf("a cell is the number %s", t)
		}
		if f == math.Trunc(f) && f >= -(1<<63) && f < 1<<63 {
			binary.LittleEndian.PutUint64(word[:], uint64(int64(f)))
			return appendSQLValue(out, sqlTagI64, word[:]), nil
		}
		binary.LittleEndian.PutUint64(word[:], math.Float64bits(f))
		return appendSQLValue(out, sqlTagF64, word[:]), nil
	case string:
		return appendSQLValue(out, sqlTagStr, []byte(t)), nil
	}
	return nil, fmt.Errorf("a cell is a %T (nested values are not SQL values)", v)
}

var typedAdapters = map[string]typedAdapter{
	// ---- storage ---------------------------------------------------------------------------------------------
	"storage.query": {out: func(call *typedCall, result any, segments [][]byte) (*typedAnswer, error) {
		rows, _ := result.([]any)
		answer := &typedAnswer{lists: map[string][]map[string]any{"records_meta": {}}, raw: map[string][]byte{}}
		var chunks [][]byte
		for _, row := range rows {
			r, _ := row.(map[string]any)
			tags, _ := r["SourceTags"].(map[string]any)
			data, _ := bytesOf(r["Data"], segments)
			chunks = append(chunks, data)
			answer.lists["records_meta"] = append(answer.lists["records_meta"], map[string]any{
				"CID": str(r["CID"]), "ROW_ID": integer(r["RowID"]), "PEER_ID": str(r["PeerID"]),
				"TIMESTAMP_MS": unixMs(r["Timestamp"]), "MATERIALIZED_MS": unixMs(r["MaterializedAt"]),
				"LENGTH": int64(len(data)), "PROVIDER_ID": str(tags["ProviderID"]), "SOURCE_NAME": str(tags["SourceName"]),
				"BATCH_ID": str(tags["BatchID"]), "PRODUCER_PEER_ID": str(tags["ProducerPeerID"]), "LICENSE": str(tags["License"]),
			})
		}
		answer.raw["records"] = sizePrefixed(chunks)
		return answer, nil
	}},
	"storage.delete": {out: noAnswer},
	"storage.ingest_with_source": {in: func(call *typedCall) (map[string]any, error) {
		request, _ := defaultTypedRequest(call)
		delete(request, "archive_source")
		delete(request, "archive_name")
		delete(request, "archive")
		request["reconcile"] = strings.ToLower(enumName("reconcileKind", call.params["RECONCILE"]))
		if raw, ok := call.raw["archive"]; ok {
			request["archive"] = map[string]any{"source": str(call.params["ARCHIVE_SOURCE"]), "name": str(call.params["ARCHIVE_NAME"]), "raw": b64(raw)}
		}
		// the "provenance" record (keys lower-cased) and the "provenance_attributes" as "attributes": [{name, value}]
		provenance, attributes := call.lists["provenance"], call.lists["provenance_attributes"]
		if len(provenance) > 0 || len(attributes) > 0 {
			document := map[string]any{}
			if len(attributes) > 0 {
				list := make([]map[string]string, 0, len(attributes))
				for _, a := range attributes {
					list = append(list, map[string]string{"name": str(a["NAME"]), "value": str(a["VALUE"])})
				}
				document["attributes"] = list
			}
			if len(provenance) > 0 {
				for k, v := range provenance[0] {
					document[strings.ToLower(k)] = v
				}
			}
			body, err := json.Marshal(document)
			if err != nil {
				return nil, typedErr("invalid-params", "provenance: %v", err)
			}
			request["provenance"] = map[string]any{"source": str(call.params["SOURCE_NAME"]), "json": b64(body)}
		}
		return request, nil
	}, out: func(call *typedCall, result any, _ [][]byte) (*typedAnswer, error) {
		object, _ := result.(map[string]any)
		record := recordFromObject("StorageIngestResult", object)
		if p := str(object["provenance"]); p != "" {
			record["PROVENANCE_FILE"] = p
		}
		return &typedAnswer{result: record}, nil
	}},
	"storage.flatsql_query_stream": {in: sqlRequest, out: streamAnswer},
	"storage.flatsql_epoch_stream": {in: func(call *typedCall) (map[string]any, error) {
		request, _ := defaultTypedRequest(call)
		return withDeliver(request, call), nil
	}, out: streamAnswer},
	// want "stream" answers like flatsql_query_stream (KIND Stream); "rows" ({kind: "rows", json}) with list "columns"
	// and raw "cells" (tagged SQL values, row-major)
	"storage.query_sandboxed": {in: func(call *typedCall) (map[string]any, error) {
		request, err := sqlRequest(call)
		if err != nil {
			return nil, err
		}
		request["want"] = strings.ToLower(enumName("sandboxWant", call.params["WANT"]))
		return request, nil
	}, out: func(call *typedCall, result any, segments [][]byte) (*typedAnswer, error) {
		object, _ := result.(map[string]any)
		if str(object["kind"]) != "rows" {
			answer, err := streamAnswer(call, result, segments)
			if err == nil {
				answer.result["KIND"] = uint8(0)
			}
			return answer, err
		}
		doc, ok := bytesOf(object["json"], segments)
		if !ok {
			return nil, typedErr("failed", "storage.query_sandboxed: rows answer carries no rows")
		}
		columns, rows, err := orderedRows(doc)
		if err != nil {
			return nil, typedErr("failed", "storage.query_sandboxed: %v", err)
		}
		answer := &typedAnswer{result: map[string]any{"KIND": uint8(1), "ROWS": int64(len(rows)), "COLUMNS": int64(len(columns))},
			lists: map[string][]map[string]any{"columns": {}}}
		for _, c := range columns {
			answer.lists["columns"] = append(answer.lists["columns"], map[string]any{"VALUE": c})
		}
		cells := []byte{}
		for _, row := range rows {
			for _, cell := range row {
				if cells, err = appendSQLCell(cells, cell); err != nil {
					return nil, typedErr("failed", "storage.query_sandboxed: %v", err)
				}
			}
		}
		answer.raw = map[string][]byte{"cells": cells}
		return answer, nil
	}},
	"storage.query_surface": {out: func(call *typedCall, result any, _ [][]byte) (*typedAnswer, error) {
		object, _ := result.(map[string]any)
		answer := &typedAnswer{lists: map[string][]map[string]any{"tables": {}, "columns": {}}}
		if caps, ok := object["caps"].(map[string]any); ok {
			answer.result = map[string]any{"TIMEOUT_MS": integer(caps["timeout_ms"]), "MAX_ROWS": integer(caps["max_rows"]), "MAX_BYTES": integer(caps["max_bytes"])}
		}
		tables, _ := object["tables"].([]any)
		for _, t := range tables {
			table, _ := t.(map[string]any)
			name := str(table["name"])
			answer.lists["tables"] = append(answer.lists["tables"], map[string]any{"NAME": name, "KIND": str(table["kind"]), "SOURCE": str(table["source"]), "RECORDS": integer(table["records"])})
			// placeholder_columns (storage.SurfaceRelation.PlaceholderColumns) names the columns that exist only for the
			// engine's >=1-column invariant: a one-byte read of slot 0, never the field's value. PLACEHOLDER carries that.
			placeholder := map[string]bool{}
			for _, p := range valuesOf(table["placeholder_columns"]) {
				placeholder[str(p)] = true
			}
			columns, _ := table["columns"].([]any)
			for _, c := range columns {
				switch col := c.(type) {
				case string:
					answer.lists["columns"] = append(answer.lists["columns"], map[string]any{"TABLE": name, "NAME": col, "PLACEHOLDER": placeholder[col]})
				case map[string]any:
					answer.lists["columns"] = append(answer.lists["columns"], map[string]any{"TABLE": name, "NAME": str(col["name"]), "PLACEHOLDER": col["placeholder"] == true})
				}
			}
		}
		return answer, nil
	}},
	"storage.flatsql_cache_key": {in: func(call *typedCall) (map[string]any, error) {
		request, _ := defaultTypedRequest(call)
		delete(request, "params") // the raw tagged values, decoded by sqlRequest
		sql, err := sqlRequest(call)
		if err != nil {
			return nil, err
		}
		for k, v := range sql {
			request[k] = v
		}
		if projection := call.lists["projection"]; projection != nil {
			names := make([]string, 0, len(projection))
			for _, p := range projection {
				names = append(names, str(p["VALUE"]))
			}
			request["projection"] = names
		}
		return request, nil
	}, out: func(call *typedCall, result any, _ [][]byte) (*typedAnswer, error) {
		object, _ := result.(map[string]any)
		return rawText("value", str(object["key"])), nil
	}},

	// ---- crypto (the node's operations) ---------------------------------------------------------------------
	"crypto.hash": {in: withAlgorithm(func(call *typedCall, r map[string]any) { rawB64(r, "data", call, "data") }), out: fieldBytes("hash", "data")},
	"crypto.sign": {in: withAlgorithm(func(call *typedCall, r map[string]any) {
		if key, ok := firstRaw(call, "key", "private_key"); ok {
			r["key"] = b64(key)
		}
		rawB64(r, "data", call, "data")
	}), out: fieldBytes("signature", "data")},
	"crypto.verify": {in: withAlgorithm(func(call *typedCall, r map[string]any) {
		if key, ok := firstRaw(call, "key", "public_key"); ok {
			r["key"] = b64(key)
		}
		rawB64(r, "data", call, "data")
		rawB64(r, "signature", call, "signature")
	}), out: boolAnswer("valid")},
	"crypto.encrypt": {in: func(call *typedCall) (map[string]any, error) {
		if _, ok := call.raw["aad"]; ok {
			return nil, typedErr("unsupported", "crypto.encrypt: additional authenticated data is not served by this host")
		}
		return withAlgorithm(func(call *typedCall, r map[string]any) {
			rawB64(r, "key", call, "key")
			rawB64(r, "nonce", call, "nonce")
			rawB64(r, "data", call, "data")
		})(call)
	}, out: func(call *typedCall, result any, segments [][]byte) (*typedAnswer, error) {
		object, _ := result.(map[string]any)
		data, _ := bytesOf(object["ciphertext"], segments)
		nonce, _ := bytesOf(object["nonce"], segments)
		return &typedAnswer{raw: map[string][]byte{"data": data, "nonce": nonce}}, nil
	}},
	"crypto.decrypt": {in: func(call *typedCall) (map[string]any, error) {
		if _, ok := call.raw["aad"]; ok {
			return nil, typedErr("unsupported", "crypto.decrypt: additional authenticated data is not served by this host")
		}
		return withAlgorithm(func(call *typedCall, r map[string]any) {
			rawB64(r, "key", call, "key")
			rawB64(r, "nonce", call, "nonce")
			rawB64(r, "ciphertext", call, "data")
		})(call)
	}, out: fieldBytes("plaintext", "data")},
	"crypto.key_agreement": {in: withAlgorithm(func(call *typedCall, r map[string]any) {
		rawB64(r, "private_key", call, "private_key")
		rawB64(r, "public_key", call, "public_key")
	}), out: fieldBytes("shared_secret", "data")},
	"crypto.kdf": {in: withAlgorithm(func(call *typedCall, r map[string]any) {
		rawB64(r, "ikm", call, "ikm")
		rawB64(r, "salt", call, "salt")
		rawB64(r, "info", call, "info")
		r["length"] = lengthOf(call, 32)
	}), out: fieldBytes("key", "data")},
	"crypto.generate_key": {in: withAlgorithm(func(*typedCall, map[string]any) {}), out: func(call *typedCall, result any, segments [][]byte) (*typedAnswer, error) {
		object, _ := result.(map[string]any)
		private, _ := bytesOf(object["private_key"], segments)
		public, _ := bytesOf(object["public_key"], segments)
		return &typedAnswer{raw: map[string][]byte{"private_key": private, "public_key": public}}, nil
	}},
	"crypto.random": {in: func(call *typedCall) (map[string]any, error) {
		n := lengthOf(call, 32)
		if n > maxRandomBytes {
			return nil, typedErr("too-large", "crypto.random: %d bytes requested, at most %d", n, maxRandomBytes)
		}
		return map[string]any{"length": n}, nil
	}, out: dataAnswer("data")},
	// crypto: the operations both hosts serve
	"crypto.ed25519.publicKeyFromSeed":      {in: rawKeys(map[string]string{"seed": "private_key"}), out: dataAnswer("data")},
	"crypto.ed25519.sign":                   {in: rawKeys(map[string]string{"seed": "private_key", "message": "data"}), out: dataAnswer("data")},
	"crypto.ed25519.verify":                 {in: rawKeys(map[string]string{"message": "data", "signature": "signature", "publicKey": "public_key"}), out: boolAnswer("result")},
	"crypto.secp256k1.publicKeyFromPrivate": {in: rawKeys(map[string]string{"privateKey": "private_key"}), out: dataAnswer("data")},
	"crypto.secp256k1.sign":                 {in: rawKeys(map[string]string{"privateKey": "private_key", "message": "data"}), out: dataAnswer("data")},
	"crypto.secp256k1.verify":               {in: rawKeys(map[string]string{"message": "data", "signature": "signature", "publicKey": "public_key"}), out: boolAnswer("result")},

	// ---- network ---------------------------------------------------------------------------------------------
	"http.request": {in: func(call *typedCall) (map[string]any, error) {
		method := str(call.params["METHOD"])
		if method == "" {
			method = "GET"
		}
		request := map[string]any{"method": method, "url": string(call.raw["url"]), "response_encoding": "binary",
			"follow_redirects": call.params["NO_REDIRECTS"] != true}
		if h, ok := call.raw["headers"]; ok {
			request["headers"] = headersFromBlock(h)
		}
		if body, ok := call.raw["body"]; ok {
			request["body"] = b64(body)
			request["body_encoding"] = "base64"
		}
		if t := num(call.params["TIMEOUT_MS"]); t > 0 {
			request["timeout_ms"] = int64(t)
		}
		if m := integer(call.params["MAX_BYTES"]); m > 0 {
			request["max_bytes"] = m
		}
		return request, nil
	}, out: func(call *typedCall, result any, segments [][]byte) (*typedAnswer, error) {
		object, _ := result.(map[string]any)
		body, ok := bytesOf(object["body"], segments)
		if !ok {
			body = []byte(str(object["body"]))
		}
		return &typedAnswer{result: map[string]any{"STATUS": integer(object["status"]), "RETRIEVED_MS": time.Now().UnixMilli()},
			raw: map[string][]byte{"url": call.raw["url"], "headers": headerBlock(object), "body": body}}, nil
	}},
	"protocol.request": {in: func(call *typedCall) (map[string]any, error) {
		request := map[string]any{"target": str(call.params["TARGET"]), "protocolId": str(call.params["PROTOCOL_ID"])}
		if t := num(call.params["TIMEOUT_MS"]); t > 0 {
			request["timeoutMs"] = int64(t)
		}
		rawB64(request, "payloadBase64", call, "payload")
		return request, nil
	}, out: dataAnswer("data")},
	"pubsub.publish": {in: func(call *typedCall) (map[string]any, error) {
		request := map[string]any{"topic": str(call.params["TOPIC"])}
		rawB64(request, "base64", call, "data")
		return request, nil
	}, out: noAnswer},
	"pubsub.subscribe": {out: func(call *typedCall, result any, _ [][]byte) (*typedAnswer, error) {
		object, _ := result.(map[string]any)
		return &typedAnswer{result: map[string]any{"ALREADY": str(object["status"]) == "already subscribed"}}, nil
	}},
	"pubsub.unsubscribe": {out: noAnswer},
	"pubsub.list_topics": {out: listOfText("topics")},
	"ipfs.cat":           {out: dataAnswer("data")},
	"ipfs.add": {in: func(call *typedCall) (map[string]any, error) {
		request := map[string]any{}
		rawB64(request, "base64", call, "data")
		if call.params["NO_PIN"] == true {
			request["pin"] = false
		}
		return request, nil
	}, out: func(call *typedCall, result any, _ [][]byte) (*typedAnswer, error) {
		object, _ := result.(map[string]any)
		size, _ := strconv.ParseInt(str(object["Size"]), 10, 64)
		return &typedAnswer{result: map[string]any{"CID": str(object["Hash"]), "NAME": str(object["Name"]), "SIZE": size}}, nil
	}},
	"ipfs.ls": {out: func(call *typedCall, result any, _ [][]byte) (*typedAnswer, error) {
		object, _ := result.(map[string]any)
		answer := &typedAnswer{lists: map[string][]map[string]any{"links": {}}}
		objects, _ := object["Objects"].([]any)
		for _, o := range objects {
			links, _ := o.(map[string]any)["Links"].([]any)
			for _, l := range links {
				link, _ := l.(map[string]any)
				answer.lists["links"] = append(answer.lists["links"], map[string]any{"NAME": str(link["Name"]), "CID": str(link["Hash"]), "SIZE": integer(link["Size"]), "TYPE": integer(link["Type"])})
			}
		}
		return answer, nil
	}},
	"ipfs.pin_add": {out: listOfText("Pins")},
	"ipfs.pin_rm":  {out: listOfText("Pins")},
	"ipfs.pin_ls": {out: func(call *typedCall, result any, _ [][]byte) (*typedAnswer, error) {
		object, _ := result.(map[string]any)
		keys, _ := object["Keys"].(map[string]any)
		answer := &typedAnswer{lists: map[string][]map[string]any{"pins": {}}}
		for _, cid := range sortedKeys(keys) {
			entry, _ := keys[cid].(map[string]any)
			answer.lists["pins"] = append(answer.lists["pins"], map[string]any{"CID": cid, "TYPE": str(entry["Type"])})
		}
		return answer, nil
	}},
	"ipfs.dag_get": {out: func(call *typedCall, result any, _ [][]byte) (*typedAnswer, error) {
		// the DAG node itself, as Kubo encodes it (dag-json)
		body, err := json.Marshal(result)
		if err != nil {
			return nil, typedErr("failed", "ipfs.dag_get: %v", err)
		}
		return &typedAnswer{raw: map[string][]byte{"data": body}}, nil
	}},
	"ipfs.name_resolve": {out: func(call *typedCall, result any, _ [][]byte) (*typedAnswer, error) {
		object, _ := result.(map[string]any)
		return &typedAnswer{result: map[string]any{"VALUE": str(object["Path"])}}, nil
	}},
	"ipfs.name_publish": {out: func(call *typedCall, result any, _ [][]byte) (*typedAnswer, error) {
		object, _ := result.(map[string]any)
		return &typedAnswer{result: map[string]any{"NAME": str(object["Name"]), "VALUE": str(object["Value"])}}, nil
	}},
	"ipfs.id": {out: func(call *typedCall, result any, _ [][]byte) (*typedAnswer, error) {
		object, _ := result.(map[string]any)
		answer := &typedAnswer{result: map[string]any{"NAME": str(object["ID"]), "VALUE": str(object["PublicKey"]),
			"AGENT_VERSION": str(object["AgentVersion"]), "PROTOCOL_VERSION": str(object["ProtocolVersion"])},
			lists: map[string][]map[string]any{"addresses": {}}}
		addresses, _ := object["Addresses"].([]any)
		for _, a := range addresses {
			answer.lists["addresses"] = append(answer.lists["addresses"], map[string]any{"VALUE": str(a)})
		}
		return answer, nil
	}},
	"ipfs.swarm_peers": {out: func(call *typedCall, result any, _ [][]byte) (*typedAnswer, error) {
		object, _ := result.(map[string]any)
		peers, _ := object["Peers"].([]any)
		answer := &typedAnswer{lists: map[string][]map[string]any{"peers": {}}}
		for _, p := range peers {
			peer, _ := p.(map[string]any)
			latency, _ := time.ParseDuration(str(peer["Latency"]))
			answer.lists["peers"] = append(answer.lists["peers"], map[string]any{"PEER_ID": str(peer["Peer"]), "ADDRESS": str(peer["Addr"]), "LATENCY_MS": float64(latency) / float64(time.Millisecond)})
		}
		return answer, nil
	}},
	"ipfs.pubsub_publish": {in: func(call *typedCall) (map[string]any, error) {
		request := map[string]any{"topic": str(call.params["TOPIC"])}
		rawB64(request, "base64", call, "data")
		return request, nil
	}, out: noAnswer},

	// ---- keyslot, secrets ------------------------------------------------------------------------------------
	"keyslot.sign": {in: func(call *typedCall) (map[string]any, error) {
		request := map[string]any{"slotId": str(call.params["SLOT_ID"])}
		if name := cryptoAlgorithmNames[enumName("cryptoAlgorithm", call.params["ALGORITHM"])]; name != "" {
			request["algorithm"] = name
		}
		rawB64(request, "payload", call, "payload")
		return request, nil
	}, out: fieldBytes("signature", "data")},
	"keyslot.unwrap": {in: func(call *typedCall) (map[string]any, error) {
		request := map[string]any{"slotId": str(call.params["SLOT_ID"])}
		rawB64(request, "ephemeralPublicKey", call, "ephemeral_public_key")
		rawB64(request, "nonce", call, "nonce")
		rawB64(request, "ciphertext", call, "ciphertext")
		return request, nil
	}, out: fieldBytes("plaintext", "data")},
	"secrets.get": {in: secretRequest, out: func(call *typedCall, result any, segments [][]byte) (*typedAnswer, error) {
		object, _ := result.(map[string]any)
		// A text credential is its UTF-8 bytes; a binary secret ({"$bin"} segment or {"__type":"bytes"}) its exact bytes.
		secret := []byte(str(object["secret"]))
		if _, binary := object["secret"].(map[string]any); binary {
			data, ok := bytesOf(object["secret"], segments)
			if !ok {
				return nil, typedErr("failed", "secrets.get: the host answered an unreadable binary secret")
			}
			secret = data
		}
		return &typedAnswer{result: map[string]any{"ID": str(object["id"]), "USERNAME": str(object["username"]), "CONFIGURED": true},
			raw: map[string][]byte{"secret": secret}}, nil
	}},
	"secrets.status": {in: secretRequest, out: secretStatus},
	"secrets.put":    {in: secretRequest, out: secretStatus},
	"secrets.clear":  {in: secretRequest, out: noAnswer},

	// ---- p2p: the frames ($EPM / $PNM, size-prefixed and concatenated) on raw "records" ------------------------
	"p2p.peers_snapshot": {in: p2pRequest, out: func(call *typedCall, result any, segments [][]byte) (*typedAnswer, error) {
		object, _ := result.(map[string]any)
		answer := &typedAnswer{result: recordFromObject("P2pSummary", object),
			lists: map[string][]map[string]any{"peers": {}, "peer_addresses": {}, "peer_standards": {}}, raw: p2pRecords(object, segments)}
		for _, peer := range objectsOf(object["peers"]) {
			id := str(peer["peer_id"])
			answer.lists["peers"] = append(answer.lists["peers"], map[string]any{"PEER_ID": id, "CONNECTED": peer["connected"] == true,
				"SELF": peer["self"] == true, "EPM_INDEX": integerOr(peer["epm_index"], -1), "AGENT_VERSION": str(peer["agent_version"])})
			for _, a := range valuesOf(peer["addrs"]) {
				answer.lists["peer_addresses"] = append(answer.lists["peer_addresses"], map[string]any{"PEER_ID": id, "VALUE": str(a)})
			}
			for _, s := range valuesOf(peer["standards"]) {
				answer.lists["peer_standards"] = append(answer.lists["peer_standards"], map[string]any{"PEER_ID": id, "VALUE": str(s)})
			}
		}
		return answer, nil
	}},
	"p2p.standards_snapshot": {in: p2pRequest, out: func(call *typedCall, result any, segments [][]byte) (*typedAnswer, error) {
		object, _ := result.(map[string]any)
		return &typedAnswer{lists: map[string][]map[string]any{"entries": pnmEntries(object)}, raw: p2pRecords(object, segments)}, nil
	}},
	"p2p.pnm_history": {in: p2pRequest, out: func(call *typedCall, result any, segments [][]byte) (*typedAnswer, error) {
		object, _ := result.(map[string]any)
		return &typedAnswer{result: recordFromObject("P2pSummary", object), lists: map[string][]map[string]any{"entries": pnmEntries(object)},
			raw: p2pRecords(object, segments)}, nil
	}},
	// {known, reason, pinned, self, fresh, standard, schema, pnm, serving: {batch_id, ..., pnm, ref | stream}}: the served
	// batch's PNM pointer when one is served, else the newest
	"p2p.latest_dataset": {in: p2pRequest, out: func(call *typedCall, result any, segments [][]byte) (*typedAnswer, error) {
		object, _ := result.(map[string]any)
		serving, _ := object["serving"].(map[string]any)
		pnm, ok := serving["pnm"].(map[string]any)
		if !ok {
			pnm, _ = object["pnm"].(map[string]any)
		}
		record := map[string]any{
			"KNOWN": object["known"] == true, "REASON": str(object["reason"]), "PINNED": object["pinned"] == true, "SELF": object["self"] == true,
			"FRESH": object["fresh"] == true, "STANDARD": str(object["standard"]), "SCHEMA": str(object["schema"]),
			"PNM_CID": str(firstOf(pnm["CID"], pnm["cid"])), "PNM_FILE_ID": str(firstOf(pnm["FILE_ID"], pnm["file_id"])),
			"PNM_PUBLISH_MS":         msOf(firstOf(pnm["PUBLISH_TIMESTAMP"], pnm["publish_timestamp"])),
			"PNM_SIGNATURE_VERIFIED": pnm["signature_verified"] == true, "PNM_ATTRIBUTION": str(pnm["attribution"]),
			"BATCH_ID": str(serving["batch_id"]), "PROVIDER_ID": str(serving["provider_id"]), "SOURCE_NAME": str(serving["source_name"]),
			"RECORD_COUNT": integer(serving["record_count"]), "BYTE_COUNT": integer(serving["byte_count"]), "PARTS": integer(serving["parts"]),
			"PUBLISHED_MS": msOf(serving["published_at"]), "ETAG_FNV1A64": str(serving["etag_fnv1a64"]),
		}
		answer := &typedAnswer{result: record, raw: map[string][]byte{}}
		if ref, ok := serving["ref"].(map[string]any); ok {
			record["REF"] = bodyRef(ref)
		} else if data, ok := bytesOf(serving["stream"], segments); ok {
			answer.raw["stream"] = data
		}
		return answer, nil
	}},

	// ---- node status and activity ----------------------------------------------------------------------------
	"node_status_read.status": {out: func(call *typedCall, result any, _ [][]byte) (*typedAnswer, error) {
		object, _ := result.(map[string]any)
		get := func(path ...string) any {
			var v any = object
			for _, p := range path {
				m, ok := v.(map[string]any)
				if !ok {
					return nil
				}
				v = m[p]
			}
			return v
		}
		record := map[string]any{
			"UPTIME_S": integer(get("uptime_seconds")), "STARTED_MS": unixMs(get("started_at")),
			"STORE_TOTAL_BYTES": integer(get("store", "total_bytes")), "STORE_TOTAL_RECORDS": integer(get("store", "total_records")),
			"STORAGE_PATH": str(get("store", "storage_path")), "SERVICE_STATE": str(get("service", "state")), "SERVICE_MODE": str(get("service", "mode")),
		}
		if disk, ok := object["disk"].(map[string]any); ok {
			record["HAS_DISK"] = true
			record["DISK_CAPACITY_BYTES"], record["DISK_FREE_BYTES"], record["DISK_AVAILABLE_BYTES"] = integer(disk["capacity_bytes"]), integer(disk["free_bytes"]), integer(disk["available_bytes"])
		}
		answer := &typedAnswer{result: record, lists: map[string][]map[string]any{"bandwidth_history": {}}}
		if bw, ok := object["bandwidth"].(map[string]any); ok {
			record["HAS_BANDWIDTH"] = true
			record["TOTAL_IN_BYTES"], record["TOTAL_OUT_BYTES"] = integer(bw["total_in_bytes"]), integer(bw["total_out_bytes"])
			record["RATE_IN_BPS"], record["RATE_OUT_BPS"] = num(bw["rate_in_bps"]), num(bw["rate_out_bps"])
			history, _ := bw["history"].([]any)
			for _, h := range history {
				s, _ := h.(map[string]any)
				answer.lists["bandwidth_history"] = append(answer.lists["bandwidth_history"], map[string]any{"TS_MS": unixMs(s["ts"]),
					"TOTAL_IN_BYTES": integer(s["total_in_bytes"]), "TOTAL_OUT_BYTES": integer(s["total_out_bytes"]), "RATE_IN_BPS": num(s["rate_in_bps"]), "RATE_OUT_BPS": num(s["rate_out_bps"])})
			}
		}
		return answer, nil
	}},
	"node_activity_read.activity": {out: func(call *typedCall, result any, _ [][]byte) (*typedAnswer, error) {
		object, _ := result.(map[string]any)
		events, _ := object["events"].([]any)
		answer := &typedAnswer{lists: map[string][]map[string]any{"events": {}}}
		for _, e := range events {
			ev, _ := e.(map[string]any)
			answer.lists["events"] = append(answer.lists["events"], map[string]any{"TS_MS": unixMs(ev["ts"]), "KIND": str(ev["kind"]), "DETAIL": str(ev["detail"]), "PEER_ID": str(ev["peer_id"])})
		}
		return answer, nil
	}},
}

func rawKeys(keys map[string]string) func(*typedCall) (map[string]any, error) {
	return func(call *typedCall) (map[string]any, error) {
		request := map[string]any{}
		for key, port := range keys {
			rawB64(request, key, call, port)
		}
		return request, nil
	}
}

func listOfText(field string) func(*typedCall, any, [][]byte) (*typedAnswer, error) {
	return func(call *typedCall, result any, _ [][]byte) (*typedAnswer, error) {
		object, _ := result.(map[string]any)
		values, _ := object[field].([]any)
		texts := make([]string, 0, len(values))
		for _, v := range values {
			texts = append(texts, str(v))
		}
		sort.Strings(texts)
		return textItems(texts), nil
	}
}

func secretRequest(call *typedCall) (map[string]any, error) {
	request := map[string]any{"id": str(call.params["ID"])}
	if u := str(call.params["USERNAME"]); u != "" {
		request["username"] = u
	}
	if s, ok := call.raw["secret"]; ok {
		request["secret"] = string(s)
	}
	return request, nil
}

func secretStatus(call *typedCall, result any, _ [][]byte) (*typedAnswer, error) {
	object, _ := result.(map[string]any)
	updated := object["updated_at"]
	if updated == nil {
		updated = object["updatedAt"]
	}
	return &typedAnswer{result: map[string]any{"ID": str(object["id"]), "CONFIGURED": object["configured"] == true || object["stored"] == true,
		"USERNAME_MASKED": str(object["username_masked"]), "UPDATED_MS": unixMs(updated), "VERIFIED_MS": unixMs(object["verified_at"])}}, nil
}

// bodyRef: a handler's {token, size, frames, fnv1a64} body reference as a BodyRef (FRAMES -1 = not counted).
func bodyRef(ref map[string]any) map[string]any {
	return map[string]any{"TOKEN": integer(ref["token"]), "SIZE": integer(ref["size"]), "FRAMES": integerOr(ref["frames"], -1), "FNV1A64": str(ref["fnv1a64"])}
}

// integerOr reads an integer answer, def when the handler left it out.
func integerOr(v any, def int64) int64 {
	if v == nil {
		return def
	}
	return integer(v)
}

func firstOf(values ...any) any {
	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return nil
}

// msOf reads an answer's time as unix ms: RFC 3339 text or a number of ms; 0 when absent or unreadable.
func msOf(v any) int64 {
	switch t := v.(type) {
	case nil:
		return 0
	case string:
		return unixMs(t)
	}
	if f, ok := toFloat(v); ok {
		return int64(f)
	}
	return 0
}

func objectsOf(v any) []map[string]any {
	items, _ := v.([]any)
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func valuesOf(v any) []any {
	items, _ := v.([]any)
	return items
}

// p2pRequest: the P2pParams fields as the handler's keys (peer_id, limit, standard), DELIVER as "ref" (Inline is
// the handler's default and is left out, as on the SDK's JS hosts).
func p2pRequest(call *typedCall) (map[string]any, error) {
	request, _ := defaultTypedRequest(call)
	return withDeliver(request, call), nil
}

func p2pRecords(object map[string]any, segments [][]byte) map[string][]byte {
	if data, ok := bytesOf(object["records"], segments); ok {
		return map[string][]byte{"records": data}
	}
	return map[string][]byte{}
}

// pnmEntries: the handler's entries as P2pPnmEntry records (the PNM's publish time as unix ms, the signature and the
// publisher key as hex).
func pnmEntries(object map[string]any) []map[string]any {
	entries := []map[string]any{}
	for _, e := range objectsOf(object["entries"]) {
		entries = append(entries, map[string]any{
			"PEER_ID": str(e["peer_id"]), "PUBLISHER_PEER_ID": str(e["publisher_peer_id"]), "GOSSIP_PEER_ID": str(e["gossip_peer_id"]),
			"STANDARD": str(e["standard"]), "SCHEMA": str(e["schema"]), "FILE_ID": str(e["file_id"]), "FILE_NAME": str(e["file_name"]),
			"CID": str(e["cid"]), "PUBLISH_MS": msOf(firstOf(e["publish_timestamp"], e["publish_ms"])), "SIGNATURE_TYPE": str(e["signature_type"]),
			"SIGNATURE_HEX": str(firstOf(e["signature"], e["signature_hex"])), "SIGNATURE_VERIFIED": e["signature_verified"] == true,
			"ATTRIBUTION": str(e["attribution"]), "PNM_INDEX": integerOr(e["pnm_index"], -1),
			"PUBLISHER_KEY_HEX": str(firstOf(e["publisher_key"], e["publisher_key_hex"])), "PUBLISHER_KEY_SOURCE": str(e["publisher_key_source"]),
		})
	}
	return entries
}

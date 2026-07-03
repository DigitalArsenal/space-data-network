package flatsqlrt

import (
	"encoding/binary"
	"fmt"
	"math"
)

// Query-parameter TLV tags (must match flatsql's decodeParams /
// wasm/standalone.js encodeQueryParams).
const (
	paramNull    = 0
	paramBool    = 1
	paramInt64   = 2
	paramFloat64 = 3
	paramString  = 4
	paramBytes   = 5
)

// EncodeParams encodes bound query parameters as the engine's TLV blob:
// per param [u8 tag][u32le payloadLen][payload]. Supported types: nil, bool,
// all Go integer types, float32/float64, string, []byte.
func EncodeParams(params []interface{}) ([]byte, error) {
	if len(params) == 0 {
		return nil, nil
	}
	var out []byte
	appendParam := func(tag byte, payload []byte) {
		var hdr [5]byte
		hdr[0] = tag
		binary.LittleEndian.PutUint32(hdr[1:], uint32(len(payload)))
		out = append(out, hdr[:]...)
		out = append(out, payload...)
	}
	appendInt64 := func(v int64) {
		var p [8]byte
		binary.LittleEndian.PutUint64(p[:], uint64(v))
		appendParam(paramInt64, p[:])
	}

	for i, v := range params {
		switch val := v.(type) {
		case nil:
			appendParam(paramNull, nil)
		case bool:
			b := byte(0)
			if val {
				b = 1
			}
			appendParam(paramBool, []byte{b})
		case int:
			appendInt64(int64(val))
		case int8:
			appendInt64(int64(val))
		case int16:
			appendInt64(int64(val))
		case int32:
			appendInt64(int64(val))
		case int64:
			appendInt64(val)
		case uint:
			appendInt64(int64(val))
		case uint8:
			appendInt64(int64(val))
		case uint16:
			appendInt64(int64(val))
		case uint32:
			appendInt64(int64(val))
		case uint64:
			if val > math.MaxInt64 {
				return nil, fmt.Errorf("flatsqlrt: param %d: uint64 %d overflows int64", i, val)
			}
			appendInt64(int64(val))
		case float32:
			var p [8]byte
			binary.LittleEndian.PutUint64(p[:], math.Float64bits(float64(val)))
			appendParam(paramFloat64, p[:])
		case float64:
			var p [8]byte
			binary.LittleEndian.PutUint64(p[:], math.Float64bits(val))
			appendParam(paramFloat64, p[:])
		case string:
			appendParam(paramString, []byte(val))
		case []byte:
			appendParam(paramBytes, val)
		default:
			return nil, fmt.Errorf("flatsqlrt: param %d: unsupported type %T", i, v)
		}
	}
	return out, nil
}

// QueryRequest is one entry in a QueryMany batch.
type QueryRequest struct {
	SQL    string
	Params []interface{}
}

// EncodeQueryRequests encodes a QueryMany batch: per request
// [u32le sqlLen][u32le paramCount][u32le paramBytesLen][sql][paramBlob]
// (must match wasm/standalone.js encodeQueryRequests).
func EncodeQueryRequests(reqs []QueryRequest) ([]byte, error) {
	var out []byte
	for _, q := range reqs {
		blob, err := EncodeParams(q.Params)
		if err != nil {
			return nil, err
		}
		var hdr [12]byte
		binary.LittleEndian.PutUint32(hdr[0:], uint32(len(q.SQL)))
		binary.LittleEndian.PutUint32(hdr[4:], uint32(len(q.Params)))
		binary.LittleEndian.PutUint32(hdr[8:], uint32(len(blob)))
		out = append(out, hdr[:]...)
		out = append(out, q.SQL...)
		out = append(out, blob...)
	}
	return out, nil
}

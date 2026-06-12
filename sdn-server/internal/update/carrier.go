package update

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

// BundleSectionName is the WASM custom section that carries the compressed
// update bundle inside the inert update.wasm carrier. The carrier is a
// storage envelope only; it is never instantiated or executed.
const BundleSectionName = "sdn.update.bundle.v1"

var wasmMagic = []byte{0x00, 0x61, 0x73, 0x6d}

// ExtractBundleFromCarrier walks the WASM module's sections without
// instantiating it and returns the payload of the SDN bundle custom section.
func ExtractBundleFromCarrier(wasmBytes []byte) ([]byte, error) {
	if len(wasmBytes) < 8 || !bytes.Equal(wasmBytes[:4], wasmMagic) {
		return nil, errors.New("update carrier is not a wasm module")
	}
	offset := 8
	for offset < len(wasmBytes) {
		sectionID := wasmBytes[offset]
		offset++
		sectionSize, n, err := readVaruint32(wasmBytes[offset:])
		if err != nil {
			return nil, fmt.Errorf("malformed update carrier: %w", err)
		}
		offset += n
		if offset+int(sectionSize) > len(wasmBytes) {
			return nil, errors.New("malformed update carrier: truncated section")
		}
		payload := wasmBytes[offset : offset+int(sectionSize)]
		offset += int(sectionSize)
		if sectionID != 0 {
			continue
		}
		nameLen, n, err := readVaruint32(payload)
		if err != nil {
			return nil, fmt.Errorf("malformed update carrier: %w", err)
		}
		if n+int(nameLen) > len(payload) {
			return nil, errors.New("malformed update carrier: truncated section name")
		}
		name := string(payload[n : n+int(nameLen)])
		if name == BundleSectionName {
			return payload[n+int(nameLen):], nil
		}
	}
	return nil, errors.New("update carrier does not contain an SDN bundle section")
}

// BuildCarrier wraps bundle bytes in a minimal valid WASM module containing
// only the SDN bundle custom section. Used by release tooling and tests; the
// canonical Node implementation lives in deployment/release.
func BuildCarrier(bundleBytes []byte) []byte {
	name := []byte(BundleSectionName)
	payload := append(appendVaruint32(nil, uint32(len(name))), name...)
	payload = append(payload, bundleBytes...)

	module := append([]byte{}, wasmMagic...)
	module = append(module, 0x01, 0x00, 0x00, 0x00)
	module = append(module, 0x00)
	module = appendVaruint32(module, uint32(len(payload)))
	return append(module, payload...)
}

func readVaruint32(data []byte) (uint32, int, error) {
	value, n := binary.Uvarint(data)
	if n <= 0 || value > 0xffffffff {
		return 0, 0, errors.New("invalid varuint32")
	}
	return uint32(value), n, nil
}

func appendVaruint32(dst []byte, value uint32) []byte {
	return binary.AppendUvarint(dst, uint64(value))
}

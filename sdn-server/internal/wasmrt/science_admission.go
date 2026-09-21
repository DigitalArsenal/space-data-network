package wasmrt

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/second-state/WasmEdge-go/wasmedge"
)

// SDN_WASM_MEMORY_BUDGETS is an operator-only map from portable artifact SHA256
// to its memory ceiling in 64-KiB pages. It grants no capability or signature
// exemption. Unlisted artifacts keep the caller's existing budget.
func artifactMemoryBudget(wasm []byte, fallback uint32) (uint32, error) {
	raw := os.Getenv("SDN_WASM_MEMORY_BUDGETS")
	if raw == "" {
		return fallback, nil
	}
	var grants map[string]uint32
	if err := json.Unmarshal([]byte(raw), &grants); err != nil {
		return 0, fmt.Errorf("invalid SDN_WASM_MEMORY_BUDGETS: %w", err)
	}
	for hash, pages := range grants {
		decoded, err := hex.DecodeString(hash)
		if err != nil || len(decoded) != 32 || pages == 0 || pages > 65536 {
			return 0, fmt.Errorf("invalid artifact memory budget: hashes must be SHA256 and pages in 1..65536")
		}
	}
	digest := sha256.Sum256(wasm)
	if pages, ok := grants[hex.EncodeToString(digest[:])]; ok {
		return pages, nil
	}
	return fallback, nil
}

// Fail before instantiation instead of allowing WasmEdge to silently truncate
// an initial memory to the configured ceiling (and trap in global constructors).
func admitMemory(ast *wasmedge.AST, pages uint32) error {
	if pages == 0 {
		return nil
	}
	check := func(value interface{}) error {
		if t, ok := value.(*wasmedge.MemoryType); ok && t.GetLimit().GetMin() > uint(pages) {
			return fmt.Errorf("WASM initial memory requires %d pages; operator budget is %d pages", t.GetLimit().GetMin(), pages)
		}
		return nil
	}
	for _, i := range ast.ListImports() {
		if err := check(i.GetExternalValue()); err != nil {
			return err
		}
	}
	for _, e := range ast.ListExports() {
		if err := check(e.GetExternalValue()); err != nil {
			return err
		}
	}
	return nil
}

// Inspect defined memories too: they need not be exported in a valid module.
// The WasmEdge validator runs first; lengths are still checked here so admission
// does not depend on a particular validator accepting the same proposals.
func admitDefinedMemory(wasm []byte, pages uint32) error {
	if pages == 0 {
		return nil
	}
	if len(wasm) < 8 {
		return fmt.Errorf("invalid WASM header")
	}
	r := bytes.NewReader(wasm[8:])
	for r.Len() > 0 {
		id, err := r.ReadByte()
		if err != nil {
			return err
		}
		size, err := binary.ReadUvarint(r)
		if err != nil || size > uint64(r.Len()) {
			return fmt.Errorf("invalid WASM section length")
		}
		if id != 5 {
			if _, err := r.Seek(int64(size), io.SeekCurrent); err != nil {
				return err
			}
			continue
		}
		section := make([]byte, int(size))
		if _, err := io.ReadFull(r, section); err != nil {
			return err
		}
		memories := bytes.NewReader(section)
		count, err := binary.ReadUvarint(memories)
		if err != nil {
			return err
		}
		for i := uint64(0); i < count; i++ {
			flags, err := binary.ReadUvarint(memories)
			if err != nil {
				return err
			}
			minimum, err := binary.ReadUvarint(memories)
			if err != nil {
				return err
			}
			if minimum > uint64(pages) {
				return fmt.Errorf("WASM initial memory requires %d pages; operator budget is %d pages", minimum, pages)
			}
			if flags&1 != 0 {
				if _, err := binary.ReadUvarint(memories); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

package sds

import (
	"embed"
	"fmt"
	"strings"
	"sync"

	"github.com/spacedatanetwork/sdn-server/internal/fbverify"
	"github.com/spacedatanetwork/sdn-server/internal/storage/recordalign"
)

// STRUCTURAL VERIFICATION ON EVERY UNTRUSTED BUFFER (security re-audit
// NET-01/NET-01b, 2026-10-15).
//
// The Go FlatBuffers runtime has no verifier: a generated accessor follows
// whatever uoffset the buffer holds and slice-indexes on it. A 32-byte $PNM
// with one field offset pointing past the end passed VerifyEnvelope (which
// checks only the root table header) and then panicked the node on an open
// gossip topic. Field-level validation through the flatc converter would
// have caught it, but that switch is off by default for cost, and a perf
// switch must never gate safety.
//
// So every buffer this node accepts is walked by internal/fbverify against
// its schema's IDL before any accessor touches it: every table, vtable,
// string, vector, struct and union offset an accessor could follow must land
// inside the buffer. It is native Go, linear in the buffer's size and always
// on. A schema it cannot compile is refused, never waved through.

// bindingSchemasFS holds the published IDL of the standards that are admitted
// from their published Go binding before the next embedded-IDL refresh
// (publishedBindingOnlySchemas). It is used ONLY to structurally verify their
// buffers; routing still keys on the binding's own file identifier.
//
//go:embed binding-schemas/*.fbs
var bindingSchemasFS embed.FS

var (
	defaultStructureOnce sync.Once
	defaultStructure     *fbverify.Registry
)

// structureSources registers every embedded standard and every published
// binding-only standard with r.
func structureSources(r *fbverify.Registry) error {
	for _, src := range []struct {
		fs  embed.FS
		dir string
	}{{schemasFS, "schemas"}, {bindingSchemasFS, "binding-schemas"}} {
		entries, err := src.fs.ReadDir(src.dir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".fbs") {
				continue
			}
			content, err := src.fs.ReadFile(src.dir + "/" + entry.Name())
			if err != nil {
				return err
			}
			r.AddSource(entry.Name(), content)
		}
	}
	return nil
}

func newStructureRegistry() *fbverify.Registry {
	r := fbverify.NewRegistry()
	if err := structureSources(r); err != nil {
		// An embed read cannot fail at runtime; if it ever did, every schema
		// would be missing and every buffer refused (fail closed).
		log.Errorf("structural verifier: embedded schemas unavailable: %v", err)
	}
	return r
}

func structureRegistry() *fbverify.Registry {
	defaultStructureOnce.Do(func() { defaultStructure = newStructureRegistry() })
	return defaultStructure
}

// VerifyStructure checks that data is a memory-safe FlatBuffer for
// schemaName in EVERY envelope form it could be read in: as a plain finished
// buffer and, when its leading u32 accounts for the rest, as a size-prefixed
// one. A form is a candidate when its root header and the schema's file
// identifier (if declared) are in place; at least one form must be a
// candidate, and every candidate must verify, because a consumer may call
// either GetRootAs or GetSizePrefixedRootAs on bytes this function passed.
func VerifyStructure(schemaName string, data []byte) error {
	return verifyStructureForms(structureRegistry(), schemaName, data)
}

// VerifySizePrefixedStructure checks data as the size-prefixed buffer that
// GetSizePrefixedRootAs<T>(data, 0) reads. Use it where the consumer is
// committed to that form (the PNM announcement lane, the EPM exchange).
func VerifySizePrefixedStructure(schemaName string, data []byte) error {
	s, err := structureRegistry().Schema(NormalizeSchemaFileName(schemaName))
	if err != nil {
		return err
	}
	if err := checkIdentifierAt(s, data, sizePrefixLength); err != nil {
		return err
	}
	return s.VerifySizePrefixedBuffer(data)
}

// VerifyBareStructure checks data as the plain finished buffer that
// GetRootAs<T>(data, 0) reads.
func VerifyBareStructure(schemaName string, data []byte) error {
	s, err := structureRegistry().Schema(NormalizeSchemaFileName(schemaName))
	if err != nil {
		return err
	}
	if err := checkIdentifierAt(s, data, 0); err != nil {
		return err
	}
	return verifyBare(s, data)
}

// verifyBare verifies a bare buffer at its alignment origin: a size-prefixed
// build whose prefix was stripped (length 4 mod 8) is aligned from 4 bytes
// before its first byte, the rule recordalign gives the reference verifier
// (Validator.placeForVerifier). Bounds are the buffer's own.
func verifyBare(s *fbverify.Schema, data []byte) error {
	return s.VerifyBufferAt(data, int64(recordalign.OriginOffset(len(data))))
}

func checkIdentifierAt(s *fbverify.Schema, data []byte, base int) error {
	ident := s.FileIdentifier()
	if ident == "" {
		return nil
	}
	if len(data) < base+minFlatBufferLength || string(data[base+sizePrefixLength:base+sizePrefixLength+fileIdentifierLength]) != ident {
		return fmt.Errorf("%w: %s: file identifier %q not found", fbverify.ErrInvalid, s.Name(), ident)
	}
	return nil
}

// A form is a candidate when a consumer could choose to read data in it.
// Generated readers choose by identifier alone: <T>BufferHasIdentifier reads
// bytes 4..8 (bare) and SizePrefixed<T>BufferHasIdentifier bytes 8..12, and
// GetSizePrefixedRootAs never checks the prefix value. So for a schema with a
// file_identifier, a form whose identifier slot holds it is a candidate
// whatever its prefix or root offset says, and it must verify (a prefix that
// does not account for the rest of the buffer fails VerifySizePrefixedBuffer).
// A schema without one is read bare unless the prefix accounts for exactly
// the rest of the buffer and the inner root is plausible.
func verifyStructureForms(r *fbverify.Registry, schemaName string, data []byte) error {
	s, err := r.Schema(schemaName)
	if err != nil {
		return err
	}
	ident := s.FileIdentifier()
	candidates := 0
	sizePrefixedCandidate := false
	if ident != "" {
		sizePrefixedCandidate = len(data) >= sizePrefixLength+minFlatBufferLength && bufferHasIdentifier(data[sizePrefixLength:], ident)
	} else if inner, ok := sizePrefixedPayload(data); ok && verifyFlatBufferRoot(inner) == nil {
		sizePrefixedCandidate = true
	}
	if sizePrefixedCandidate {
		candidates++
		if err := s.VerifySizePrefixedBuffer(data); err != nil {
			return fmt.Errorf("size-prefixed form: %w", err)
		}
	}
	bareCandidate := false
	if ident != "" {
		bareCandidate = bufferHasIdentifier(data, ident)
	} else {
		bareCandidate = verifyFlatBufferRoot(data) == nil
	}
	if bareCandidate {
		candidates++
		if err := verifyBare(s, data); err != nil {
			return fmt.Errorf("bare form: %w", err)
		}
	}
	if candidates == 0 {
		return fmt.Errorf("%w: %s: neither a bare nor a size-prefixed buffer", fbverify.ErrInvalid, schemaName)
	}
	return nil
}

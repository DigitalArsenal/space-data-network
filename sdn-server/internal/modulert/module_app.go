package modulert

// APP belongs to the SDK bundle. The host validates and serves its existing
// record; it never generates an application-specific page or invocation.
import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	APPfb "github.com/DigitalArsenal/spacedatastandards.org/lib/go/APP"
)

var ErrNoModuleApplication = errors.New("module has no embedded application")

// ApplicationRecord returns one verified, size-prefixed SDS APP from the
// loaded bundle. An APP must bind its owning module and every inline page hash.
func (m *Module) ApplicationRecord() (record []byte, err error) {
	defer func() {
		if recover() != nil {
			record = nil
			err = errors.New("invalid embedded application record")
		}
	}()
	m.mu.Lock()
	if m.mod == nil || m.paused || m.manifest == nil {
		m.mu.Unlock()
		return nil, errors.New("module is not running")
	}
	artifact := append([]byte(nil), m.wasmBytes...)
	pluginID := m.manifest.PluginID
	m.mu.Unlock()
	trailer := PublicationTrailerRecordBytes(artifact)
	if len(trailer) == 0 {
		return nil, ErrNoModuleApplication
	}
	bundle, err := decodeModuleBundle(trailer, StripPublicationTrailer(artifact))
	if err != nil {
		return nil, err
	}
	for _, entry := range bundle.entries {
		if entry.sectionName != "sdn.app.record" {
			continue
		}
		if record != nil {
			return nil, errors.New("module bundle contains multiple application records")
		}
		record = entry.payload
	}
	if record == nil {
		return nil, ErrNoModuleApplication
	}
	if len(record) < 12 || len(record) > 16<<20 || int(binary.LittleEndian.Uint32(record)) != len(record)-4 || !APPfb.SizePrefixedAPPBufferHasIdentifier(record) {
		return nil, errors.New("module application is not a bounded SDS APP record")
	}
	app := APPfb.GetSizePrefixedRootAsAPP(record, 0)
	if len(app.ID()) == 0 {
		return nil, errors.New("module application has no ID")
	}
	if app.ModulesLength() > 256 || app.UILength() > 64 {
		return nil, errors.New("application exceeds module or page limits")
	}
	owner := false
	for i := 0; i < app.ModulesLength(); i++ {
		var ref APPfb.APPModuleRef
		if !app.MODULES(&ref, i) {
			return nil, errors.New("invalid application module reference")
		}
		if string(ref.PLUGIN_ID()) == pluginID {
			if string(ref.CONTENT_HASH()) != hex.EncodeToString(bundle.canonicalModuleHash) {
				return nil, errors.New("application references a different module artifact")
			}
			owner = true
		}
	}
	if !owner {
		return nil, errors.New("application does not reference its owning module")
	}
	entries := 0
	decodedSize := 0
	for i := 0; i < app.UILength(); i++ {
		var page APPfb.APPUIPage
		if !app.UI(&page, i) {
			return nil, errors.New("invalid application page")
		}
		if page.ENTRY() {
			entries++
		}
		if strings.TrimSpace(strings.ToLower(strings.SplitN(string(page.MEDIA_TYPE()), ";", 2)[0])) != "text/html" || len(page.CONTENT()) == 0 || len(page.URL()) != 0 {
			return nil, errors.New("application entry must be a self-contained HTML page")
		}
		content, err := decodeApplicationPage(&page)
		if err != nil {
			return nil, err
		}
		decodedSize += len(content)
		if decodedSize > 16<<20 {
			return nil, errors.New("decoded application exceeds the page limit")
		}
		digest := sha256.Sum256(content)
		if string(page.CONTENT_SHA256()) != hex.EncodeToString(digest[:]) {
			return nil, fmt.Errorf("application page %q failed its content hash check", page.ID())
		}
	}
	if entries != 1 {
		return nil, errors.New("application must have exactly one entry page")
	}
	return append([]byte(nil), record...), nil
}

// ApplicationArtifact returns the same canonical payload used by the native
// runtime. Access is limited to an authenticated local-node UI by the API.
func (m *Module) ApplicationArtifact() ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.mod == nil || m.paused {
		return nil, errors.New("module is not running")
	}
	return CanonicalArtifactBytes(m.wasmBytes)
}

// CanonicalArtifactBytes validates a bundle while selecting its executable
// identity. Callers retain the original bundle for APP pages and signatures.
func CanonicalArtifactBytes(artifact []byte) ([]byte, error) {
	portable := StripPublicationTrailer(artifact)
	prefix := "sds."
	if trailer := PublicationTrailerRecordBytes(artifact); len(trailer) != 0 {
		bundle, err := decodeModuleBundle(trailer, portable)
		if err != nil {
			return nil, err
		}
		if value, ok := bundle.customSectionPrefix.(string); ok {
			prefix = value
		}
	}
	return stripWasmCustomSectionsWithPrefix(portable, prefix)
}

func (m *Module) ApplicationPage() ([]byte, error) {
	record, err := m.ApplicationRecord()
	if err != nil {
		return nil, err
	}
	app := APPfb.GetSizePrefixedRootAsAPP(record, 0)
	for i := 0; i < app.UILength(); i++ {
		var page APPfb.APPUIPage
		if app.UI(&page, i) && page.ENTRY() {
			return decodeApplicationPage(&page)
		}
	}
	return nil, ErrNoModuleApplication
}

func decodeApplicationPage(page *APPfb.APPUIPage) ([]byte, error) {
	content := page.CONTENT()
	switch page.ENCODING() {
	case 0: // SDS UTF8
	case 1, 2: // SDS BASE64 / BASE64_GZIP
		decoded, err := base64.StdEncoding.Strict().DecodeString(string(content))
		if err != nil {
			return nil, errors.New("invalid base64 application page")
		}
		content = decoded
		if page.ENCODING() == 2 {
			reader, err := gzip.NewReader(bytes.NewReader(content))
			if err != nil {
				return nil, errors.New("invalid compressed application page")
			}
			defer reader.Close()
			content, err = io.ReadAll(io.LimitReader(reader, (16<<20)+1))
			if err != nil {
				return nil, errors.New("invalid compressed application page")
			}
		}
	default:
		return nil, errors.New("unsupported application page encoding")
	}
	if len(content) > 16<<20 || !utf8.Valid(content) {
		return nil, errors.New("application page exceeds its limit or is not UTF-8")
	}
	return append([]byte(nil), content...), nil
}

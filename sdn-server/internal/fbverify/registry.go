package fbverify

import (
	"fmt"
	"sync"
)

// Registry compiles schemas from their IDL sources on first use and caches
// the result (or the compile error, so an uncompilable schema is refused
// every time without being re-parsed). It is safe for concurrent use.
type Registry struct {
	mu       sync.RWMutex
	sources  map[string][]byte
	compiled map[string]*Schema
	errs     map[string]error
	resolve  func(includePath, fromName string) string
}

// NewRegistry returns an empty registry using DefaultIncludeResolver.
func NewRegistry() *Registry {
	return &Registry{
		sources:  map[string][]byte{},
		compiled: map[string]*Schema{},
		errs:     map[string]error{},
		resolve:  DefaultIncludeResolver,
	}
}

// AddSource registers (or replaces) a schema's IDL. Every compiled schema is
// dropped, since any of them may include this one.
func (r *Registry) AddSource(name string, idl []byte) {
	cp := append([]byte(nil), idl...)
	r.mu.Lock()
	r.sources[name] = cp
	r.compiled = map[string]*Schema{}
	r.errs = map[string]error{}
	r.mu.Unlock()
}

// HasSource reports whether name has an IDL source.
func (r *Registry) HasSource(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.sources[name]
	return ok
}

// Schema returns the compiled schema for name.
func (r *Registry) Schema(name string) (*Schema, error) {
	r.mu.RLock()
	s, ok := r.compiled[name]
	err := r.errs[name]
	r.mu.RUnlock()
	if ok {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.compiled[name]; ok {
		return s, nil
	}
	if err := r.errs[name]; err != nil {
		return nil, err
	}
	s, err = compileSchema(name, func(n string) ([]byte, bool) {
		src, ok := r.sources[n]
		return src, ok
	}, r.resolve)
	if err != nil {
		err = fmt.Errorf("fbverify: cannot compile schema %s: %w", name, err)
		r.errs[name] = err
		return nil, err
	}
	r.compiled[name] = s
	return s, nil
}

// VerifyBuffer verifies a plain finished buffer against schema name.
func (r *Registry) VerifyBuffer(name string, buf []byte) error {
	s, err := r.Schema(name)
	if err != nil {
		return err
	}
	return s.VerifyBuffer(buf)
}

// VerifySizePrefixedBuffer verifies a size-prefixed buffer against schema name.
func (r *Registry) VerifySizePrefixedBuffer(name string, buf []byte) error {
	s, err := r.Schema(name)
	if err != nil {
		return err
	}
	return s.VerifySizePrefixedBuffer(buf)
}

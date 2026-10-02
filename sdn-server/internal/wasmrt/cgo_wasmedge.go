package wasmrt

// Two WasmEdge C-API calls the Go binding (WasmEdge-go v0.14.0) does not
// expose, reached through the binding's own C handles:
//
//   - WasmEdge_ConfigureCompilerSetInterruptible, so AOT code checks the
//     executor's stop token (design A21: a stop must reach service threads in
//     compute loops, and A30: format 2 refuses anything else);
//   - the WasmEdge_ModuleInstanceContext behind a *wasmedge.Module, so the C
//     host I/O module can add its functions to "env" (design §5.4).
//
// The binding keeps each C handle in its struct's first field, `_inner`. That
// layout is asserted by reflection before any pointer is taken; a binding
// upgrade that moves it makes these calls fail closed with ErrBindingLayout
// instead of reading the wrong word.

/*
#include <stdbool.h>
#include <wasmedge/wasmedge.h>

static void sdn_conf_set_interruptible(void *c, int on) {
	WasmEdge_ConfigureCompilerSetInterruptible((WasmEdge_ConfigureContext *)c, on != 0);
}
static int sdn_conf_is_interruptible(void *c) {
	return WasmEdge_ConfigureCompilerIsInterruptible((const WasmEdge_ConfigureContext *)c) ? 1 : 0;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"unsafe"

	"github.com/second-state/WasmEdge-go/wasmedge"
)

// ErrBindingLayout reports that a WasmEdge-go struct no longer keeps its C
// handle where this package reads it.
var ErrBindingLayout = errors.New("wasmrt: WasmEdge-go struct layout changed; native handle unavailable")

// innerHandle returns the C handle held in the first field of a WasmEdge-go
// wrapper, after checking the field's name, offset, kind and C type.
func innerHandle(wrapper interface{}, ctype string) (unsafe.Pointer, error) {
	rv := reflect.ValueOf(wrapper)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return nil, fmt.Errorf("%w: nil %T", ErrBindingLayout, wrapper)
	}
	t := rv.Elem().Type()
	if t.Kind() != reflect.Struct || t.NumField() == 0 {
		return nil, fmt.Errorf("%w: %s is not a struct", ErrBindingLayout, t)
	}
	f := t.Field(0)
	if f.Name != "_inner" || f.Offset != 0 || f.Type.Kind() != reflect.Ptr ||
		!strings.Contains(f.Type.Elem().Name(), ctype) {
		return nil, fmt.Errorf("%w: %s.%s is %s", ErrBindingLayout, t, f.Name, f.Type)
	}
	p := *(*unsafe.Pointer)(unsafe.Pointer(rv.Pointer()))
	if p == nil {
		return nil, fmt.Errorf("%w: %s has a nil handle", ErrBindingLayout, t)
	}
	return p, nil
}

// ModuleInstanceContext returns the WasmEdge_ModuleInstanceContext* behind m.
func ModuleInstanceContext(m *wasmedge.Module) (unsafe.Pointer, error) {
	return innerHandle(m, "WasmEdge_ModuleInstanceContext")
}

// SetCompilerInterruptible makes AOT code compiled with conf check the stop
// token at every loop header and function entry (block entries too on a
// runtime without 05-loop-stop-checks), so a cancel can stop a guest that
// never calls out.
func SetCompilerInterruptible(conf *wasmedge.Configure, on bool) error {
	p, err := innerHandle(conf, "WasmEdge_ConfigureContext")
	if err != nil {
		return err
	}
	v := C.int(0)
	if on {
		v = 1
	}
	C.sdn_conf_set_interruptible(p, v)
	if (C.sdn_conf_is_interruptible(p) != 0) != on {
		return errors.New("wasmrt: WasmEdge refused the interruptible compiler setting")
	}
	return nil
}

// NewThreadedCompilerConfig is the AOT compile configuration of a threaded
// instance: the THREADS proposal the runtime enables, plus Interruptible.
func NewThreadedCompilerConfig() (*wasmedge.Configure, error) {
	conf := wasmedge.NewConfigure(wasmedge.THREADS)
	if conf == nil {
		return nil, errors.New("wasmrt: cannot create a WasmEdge configuration")
	}
	if err := SetCompilerInterruptible(conf, true); err != nil {
		conf.Release()
		return nil, err
	}
	return conf, nil
}

package flowrt

import (
	"crypto/rand"
	"strings"
	"time"

	"github.com/ipfs/kubo/sdn/wasmrt"
	"github.com/second-state/WasmEdge-go/wasmedge"
)

// buildFlowHostFuncs returns host functions for the "sdn" or "env" module,
// shared with wasiplugin but kept here to avoid a circular dependency.
func buildFlowHostFuncs(name string) []wasmrt.HostFunc {
	i32 := func() *wasmedge.ValType { return wasmedge.NewValTypeI32() }
	i64 := func() *wasmedge.ValType { return wasmedge.NewValTypeI64() }

	funcs := []wasmrt.HostFunc{
		{
			Name: "clock_now_ms",
			Func: func(_ interface{}, _ *wasmedge.CallingFrame, _ []interface{}) ([]interface{}, wasmedge.Result) {
				return []interface{}{time.Now().UnixMilli()}, wasmedge.Result_Success
			},
			Returns: []*wasmedge.ValType{i64()},
		},
		{
			Name: "random_bytes",
			Func: func(_ interface{}, cf *wasmedge.CallingFrame, params []interface{}) ([]interface{}, wasmedge.Result) {
				ptr := uint32(params[0].(int32))
				length := uint32(params[1].(int32))
				const maxRandomBytes = 8192
				if length > maxRandomBytes {
					return []interface{}{int32(-1)}, wasmedge.Result_Success
				}
				buf := make([]byte, length)
				if _, err := rand.Read(buf); err != nil {
					return []interface{}{int32(-1)}, wasmedge.Result_Success
				}
				mem := cf.GetMemoryByIndex(0)
				if mem == nil {
					return []interface{}{int32(-1)}, wasmedge.Result_Success
				}
				if err := mem.SetData(buf, uint(ptr), uint(length)); err != nil {
					return []interface{}{int32(-1)}, wasmedge.Result_Success
				}
				return []interface{}{int32(0)}, wasmedge.Result_Success
			},
			Params:  []*wasmedge.ValType{i32(), i32()},
			Returns: []*wasmedge.ValType{i32()},
		},
		{
			Name: "log",
			Func: func(_ interface{}, cf *wasmedge.CallingFrame, params []interface{}) ([]interface{}, wasmedge.Result) {
				level := params[0].(int32)
				ptr := uint32(params[1].(int32))
				length := uint32(params[2].(int32))
				const maxLogLen = 4096
				if length > maxLogLen {
					length = maxLogLen
				}
				mem := cf.GetMemoryByIndex(0)
				if mem == nil {
					return nil, wasmedge.Result_Success
				}
				data, err := mem.GetData(uint(ptr), uint(length))
				if err != nil {
					return nil, wasmedge.Result_Success
				}
				msg := strings.Map(func(r rune) rune {
					if r < 0x20 && r != ' ' {
						return '?'
					}
					return r
				}, string(data))
				switch {
				case level <= 0:
					log.Debugf("[flow] %s", msg)
				case level == 1:
					log.Infof("[flow] %s", msg)
				case level == 2:
					log.Warnf("[flow] %s", msg)
				default:
					log.Errorf("[flow] %s", msg)
				}
				return nil, wasmedge.Result_Success
			},
			Params: []*wasmedge.ValType{i32(), i32(), i32()},
		},
	}

	if name == "env" {
		funcs = append(funcs, buildExceptionStubs()...)
	}

	return funcs
}

// buildExceptionStubs returns C++ exception handling stubs for the "env" module.
func buildExceptionStubs() []wasmrt.HostFunc {
	i32 := func() *wasmedge.ValType { return wasmedge.NewValTypeI32() }
	i64 := func() *wasmedge.ValType { return wasmedge.NewValTypeI64() }

	i32n := func(n int) []*wasmedge.ValType {
		v := make([]*wasmedge.ValType, n)
		for j := range v {
			v[j] = i32()
		}
		return v
	}

	ret0 := func(name string, nParams int) wasmrt.HostFunc {
		return wasmrt.HostFunc{
			Name: name,
			Func: func(_ interface{}, _ *wasmedge.CallingFrame, _ []interface{}) ([]interface{}, wasmedge.Result) {
				return []interface{}{int32(0)}, wasmedge.Result_Success
			},
			Params:  i32n(nParams),
			Returns: []*wasmedge.ValType{i32()},
		}
	}
	noop := func(name string, params []*wasmedge.ValType) wasmrt.HostFunc {
		return wasmrt.HostFunc{
			Name: name,
			Func: func(_ interface{}, _ *wasmedge.CallingFrame, _ []interface{}) ([]interface{}, wasmedge.Result) {
				return nil, wasmedge.Result_Success
			},
			Params: params,
		}
	}

	return []wasmrt.HostFunc{
		noop("invoke_vi", i32n(2)),
		ret0("__cxa_find_matching_catch_3", 1),
		ret0("invoke_iii", 3),
		{Name: "__cxa_find_matching_catch_2", Func: func(_ interface{}, _ *wasmedge.CallingFrame, _ []interface{}) ([]interface{}, wasmedge.Result) {
			return []interface{}{int32(0)}, wasmedge.Result_Success
		}, Returns: []*wasmedge.ValType{i32()}},
		noop("__resumeException", i32n(1)),
		ret0("invoke_iiiiii", 6),
		noop("invoke_viiiii", i32n(6)),
		ret0("invoke_iiiii", 5),
		{Name: "invoke_j", Func: func(_ interface{}, _ *wasmedge.CallingFrame, _ []interface{}) ([]interface{}, wasmedge.Result) {
			return []interface{}{int64(0)}, wasmedge.Result_Success
		}, Params: i32n(1), Returns: []*wasmedge.ValType{i64()}},
		ret0("invoke_iiii", 4),
		noop("invoke_vijjj", []*wasmedge.ValType{i32(), i32(), i64(), i64(), i64()}),
		noop("invoke_viiii", i32n(5)),
		noop("invoke_viii", i32n(4)),
		ret0("invoke_ii", 2),
		noop("invoke_v", i32n(1)),
		ret0("invoke_i", 1),
		ret0("invoke_iiiiiiii", 8),
		noop("invoke_vii", i32n(3)),
		noop("invoke_viiiiii", i32n(7)),
		ret0("llvm_eh_typeid_for", 1),
		ret0("__cxa_begin_catch", 1),
		noop("__cxa_end_catch", nil),
		noop("__throw_exception_with_stack_trace", i32n(1)),
		ret0("invoke_iiiiiiiiii", 10),
	}
}

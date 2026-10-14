// Package runtimecfg is the module config a host hands a module: plugin.getConfig answers raw "config" with one
// ModuleConfig FlatBuffer (the SDK's schemas/runtime/ModuleConfig.fbs, file identifier MCFG, not size-prefixed).
// moduleconfig_generated.go holds the flatc bindings (generate.mjs); this file builds one from a node config.
package runtimecfg

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"

	flatbuffers "github.com/google/flatbuffers/go"
)

// The value kinds (configValueKind) by name.
const (
	KindString     = configValueKindString
	KindNumber     = configValueKindNumber
	KindBool       = configValueKindBool
	KindStringList = configValueKindStringList
	KindNumberList = configValueKindNumberList
	KindTableList  = configValueKindTableList
)

// Encode builds the ModuleConfig of a node config (NodeContext.Config, as the node config file's
// flows.services[].config decodes), the way the SDK's encodeModuleConfig does: strings, numbers and booleans as
// themselves; a list of numbers a NumberList (an empty list too), of strings a StringList, of objects a TableList;
// a nested object a TableList of one table; keys sorted. Unset values (nil) are left out. A value with no config kind
// (a list mixing kinds, a list of lists, a channel, ...) is an error naming its path.
func Encode(config map[string]interface{}) ([]byte, error) {
	b := flatbuffers.NewBuilder(1024)
	entries, err := encodeEntries(b, reflect.ValueOf(config), "")
	if err != nil {
		return nil, err
	}
	ModuleConfigStart(b)
	ModuleConfigAddEntries(b, entries)
	FinishModuleConfigBuffer(b, ModuleConfigEnd(b))
	return b.FinishedBytes(), nil
}

// value is v with interfaces and pointers looked through (invalid for nil).
func value(v reflect.Value) reflect.Value {
	for v.IsValid() && (v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer) {
		if v.IsNil() {
			return reflect.Value{}
		}
		v = v.Elem()
	}
	return v
}

func number(v reflect.Value) (float64, bool) {
	if v.Type() == reflect.TypeOf(json.Number("")) {
		n, err := json.Number(v.String()).Float64()
		return n, err == nil
	}
	switch v.Kind() {
	case reflect.Float32, reflect.Float64:
		return v.Float(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(v.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return float64(v.Uint()), true
	}
	return 0, false
}

func isObject(v reflect.Value) bool {
	return v.Kind() == reflect.Map && v.Type().Key().Kind() == reflect.String
}

// encodeEntries writes the entries of an object (a map with string keys), sorted by key, and returns their vector.
func encodeEntries(b *flatbuffers.Builder, object reflect.Value, path string) (flatbuffers.UOffsetT, error) {
	object = value(object)
	var keys []string
	if object.IsValid() {
		if !isObject(object) {
			return 0, fmt.Errorf("%s is a %s, not an object", where(path), object.Type())
		}
		for _, k := range object.MapKeys() {
			if value(object.MapIndex(k)).IsValid() {
				keys = append(keys, k.String())
			}
		}
	}
	sort.Strings(keys)
	offsets := make([]flatbuffers.UOffsetT, len(keys))
	for i, key := range keys {
		o, err := encodeEntry(b, key, value(object.MapIndex(reflect.ValueOf(key).Convert(object.Type().Key()))), join(path, key))
		if err != nil {
			return 0, err
		}
		offsets[i] = o
	}
	ModuleConfigStartEntriesVector(b, len(offsets))
	for i := len(offsets) - 1; i >= 0; i-- {
		b.PrependUOffsetT(offsets[i])
	}
	return b.EndVector(len(offsets)), nil
}

func encodeEntry(b *flatbuffers.Builder, key string, v reflect.Value, path string) (flatbuffers.UOffsetT, error) {
	var kind configValueKind
	var stringValue, strings, numbers, tables flatbuffers.UOffsetT
	var numberValue float64
	var boolValue bool
	switch {
	case v.Kind() == reflect.String && v.Type() != reflect.TypeOf(json.Number("")):
		kind, stringValue = KindString, b.CreateString(v.String())
	case v.Kind() == reflect.Bool:
		kind, boolValue = KindBool, v.Bool()
	case isObject(v):
		kind = KindTableList
		table, err := encodeTable(b, v, path)
		if err != nil {
			return 0, err
		}
		ConfigEntryStartTablesVector(b, 1)
		b.PrependUOffsetT(table)
		tables = b.EndVector(1)
	case v.Kind() == reflect.Slice || v.Kind() == reflect.Array:
		var err error
		kind, strings, numbers, tables, err = encodeList(b, v, path)
		if err != nil {
			return 0, err
		}
	default:
		n, ok := number(v)
		if !ok {
			return 0, fmt.Errorf("%s has no config value kind (%s)", where(path), v.Type())
		}
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return 0, fmt.Errorf("%s is not a finite number", where(path))
		}
		kind, numberValue = KindNumber, n
	}
	k := b.CreateString(key)
	ConfigEntryStart(b)
	ConfigEntryAddKey(b, k)
	ConfigEntryAddKind(b, kind)
	switch kind {
	case KindString:
		ConfigEntryAddStringValue(b, stringValue)
	case KindNumber:
		ConfigEntryAddNumberValue(b, numberValue)
	case KindBool:
		ConfigEntryAddBoolValue(b, boolValue)
	case KindStringList:
		ConfigEntryAddStrings(b, strings)
	case KindNumberList:
		ConfigEntryAddNumbers(b, numbers)
	case KindTableList:
		ConfigEntryAddTables(b, tables)
	}
	return ConfigEntryEnd(b), nil
}

func encodeTable(b *flatbuffers.Builder, object reflect.Value, path string) (flatbuffers.UOffsetT, error) {
	entries, err := encodeEntries(b, object, path)
	if err != nil {
		return 0, err
	}
	ConfigTableStart(b)
	ConfigTableAddEntries(b, entries)
	return ConfigTableEnd(b), nil
}

// encodeList writes a list (its nil items left out) as a NumberList, StringList or TableList.
func encodeList(b *flatbuffers.Builder, list reflect.Value, path string) (configValueKind, flatbuffers.UOffsetT, flatbuffers.UOffsetT, flatbuffers.UOffsetT, error) {
	items := make([]reflect.Value, 0, list.Len())
	for i := 0; i < list.Len(); i++ {
		if item := value(list.Index(i)); item.IsValid() {
			items = append(items, item)
		}
	}
	all := func(test func(reflect.Value) bool) bool {
		for _, item := range items {
			if !test(item) {
				return false
			}
		}
		return true
	}
	isNumber := func(v reflect.Value) bool {
		_, ok := number(v)
		return ok
	}
	isString := func(v reflect.Value) bool {
		return v.Kind() == reflect.String && v.Type() != reflect.TypeOf(json.Number(""))
	}
	switch {
	case all(isNumber):
		values := make([]float64, len(items))
		for i, item := range items {
			values[i], _ = number(item)
			if math.IsNaN(values[i]) || math.IsInf(values[i], 0) {
				return 0, 0, 0, 0, fmt.Errorf("%s.%d is not a finite number", where(path), i)
			}
		}
		ConfigEntryStartNumbersVector(b, len(values))
		for i := len(values) - 1; i >= 0; i-- {
			b.PrependFloat64(values[i])
		}
		return KindNumberList, 0, b.EndVector(len(values)), 0, nil
	case all(isObject):
		offsets := make([]flatbuffers.UOffsetT, len(items))
		for i, item := range items {
			o, err := encodeTable(b, item, fmt.Sprintf("%s.%d", path, i))
			if err != nil {
				return 0, 0, 0, 0, err
			}
			offsets[i] = o
		}
		ConfigEntryStartTablesVector(b, len(offsets))
		for i := len(offsets) - 1; i >= 0; i-- {
			b.PrependUOffsetT(offsets[i])
		}
		return KindTableList, 0, 0, b.EndVector(len(offsets)), nil
	case all(isString):
		offsets := make([]flatbuffers.UOffsetT, len(items))
		for i, item := range items {
			offsets[i] = b.CreateString(item.String())
		}
		ConfigEntryStartStringsVector(b, len(offsets))
		for i := len(offsets) - 1; i >= 0; i-- {
			b.PrependUOffsetT(offsets[i])
		}
		return KindStringList, b.EndVector(len(offsets)), 0, 0, nil
	}
	return 0, 0, 0, 0, fmt.Errorf("%s mixes value kinds", where(path))
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func where(path string) string {
	if path == "" {
		return "the config"
	}
	return path
}

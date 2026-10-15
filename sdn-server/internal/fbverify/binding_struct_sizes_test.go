package fbverify

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// collectStructs gathers every struct a table field or vector of t can hold,
// keyed by its unqualified name.
func collectStructs(t *tableDef, seen map[*tableDef]bool, out map[string]*structDef) {
	if t == nil || seen[t] {
		return
	}
	seen[t] = true
	var visit func(ft *fieldType)
	visit = func(ft *fieldType) {
		if ft == nil {
			return
		}
		switch ft.base {
		case tStruct:
			name := ft.strct.name
			if i := strings.LastIndex(name, "."); i >= 0 {
				name = name[i+1:]
			}
			out[name] = ft.strct
		case tTable:
			collectStructs(ft.table, seen, out)
		case tUnion:
			for _, m := range ft.union.members {
				collectStructs(m, seen, out)
			}
		case tVector:
			visit(ft.elem)
		}
	}
	for i := range t.fields {
		visit(&t.fields[i].typ)
	}
}

var createPrep = regexp.MustCompile(`(?m)^func Create(\w+)\(builder \*flatbuffers\.Builder[^\n]*\n\s*builder\.Prep\((\d+), (\d+)\)`)

// A struct is bounds-checked as a whole, so its size is the one number the
// verifier must get exactly right: one byte short and a struct at the end of
// a buffer reads past it. Every struct the published Go bindings construct
// (builder.Prep(align, size) in Create<Struct>) must have the same size and
// alignment in the verifier's model of the embedded IDL.
func TestStructSizesMatchThePublishedBindings(t *testing.T) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/DigitalArsenal/spacedatastandards.org/lib/go").Output()
	if err != nil {
		t.Fatalf("locate the published Go bindings: %v", err)
	}
	bindingDir := strings.TrimSpace(string(out))
	files, err := filepath.Glob("../sds/schemas/*.fbs")
	if err != nil || len(files) < 200 {
		t.Fatalf("embedded schemas: %d (%v)", len(files), err)
	}
	r := NewRegistry()
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		r.AddSource(filepath.Base(f), b)
	}
	compared := 0
	for _, f := range files {
		name := filepath.Base(f)
		pkg := strings.TrimSuffix(name, ".fbs")
		sources, _ := filepath.Glob(filepath.Join(bindingDir, pkg, "*.go"))
		if len(sources) == 0 {
			continue
		}
		s, err := r.Schema(name)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		structs := map[string]*structDef{}
		collectStructs(s.root, map[*tableDef]bool{}, structs)
		for _, src := range sources {
			code, err := os.ReadFile(src)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range createPrep.FindAllStringSubmatch(string(code), -1) {
				sd, ok := structs[m[1]]
				if !ok {
					continue // not reachable from this root
				}
				align, _ := strconv.ParseInt(m[2], 10, 64)
				size, _ := strconv.ParseInt(m[3], 10, 64)
				compared++
				if sd.size != size || sd.align != align {
					t.Errorf("%s: struct %s is %d bytes aligned %d in the verifier, %d aligned %d in the binding", name, m[1], sd.size, sd.align, size, align)
				}
			}
		}
	}
	if compared == 0 {
		t.Fatal("no struct was compared; the test proves nothing")
	}
	t.Logf("%d struct layouts match their bindings", compared)
}

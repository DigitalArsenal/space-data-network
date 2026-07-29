// boxspike — JANUS Phase 1 spike: compile a guest TU through llvm-box.wasm
// under NATIVE WasmEdge (the node's own runtime) and report the object's
// sha256, so it can be byte-diffed against the Node/browser lanes.
//
// Not shipped. Scratch harness for the sdk-isomorphic-toolchain feasibility
// spike; lives on branch janus/boxcc-spike only.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ipfs/kubo/sdn/flowcc"
)

func main() {
	box := os.Getenv("BOX")
	sysroot := os.Getenv("SYSROOT")
	tu := os.Getenv("TU")
	hdr := os.Getenv("HDR")
	if box == "" || sysroot == "" || tu == "" {
		fmt.Fprintln(os.Stderr, "need BOX, SYSROOT, TU (HDR optional)")
		os.Exit(2)
	}

	t0 := time.Now()
	c, err := flowcc.NewWithSysroot(box, sysroot)
	if err != nil {
		fmt.Fprintln(os.Stderr, "new:", err)
		os.Exit(1)
	}
	loadMs := time.Since(t0).Milliseconds()

	src, err := os.ReadFile(tu)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read tu:", err)
		os.Exit(1)
	}
	in := map[string][]byte{"/work/" + filepath.Base(tu): src}
	if hdr != "" {
		h, err := os.ReadFile(hdr)
		if err != nil {
			fmt.Fprintln(os.Stderr, "read hdr:", err)
			os.Exit(1)
		}
		in["/work/"+filepath.Base(hdr)] = h
	}

	args := []string{
		"clang", "clang",
		"--target=wasm32-wasip1-threads", "--sysroot=/sysroot",
		"-c", "/work/" + filepath.Base(tu), "-I/work",
		"-O3", "-mbulk-memory", "-DNDEBUG", "-matomics", "-fno-exceptions", "-pthread",
		"-o", "/work/out.o",
	}

	type lane struct {
		Run      int    `json:"run"`
		RC       int    `json:"rc"`
		MS       int64  `json:"ms"`
		Bytes    int    `json:"bytes"`
		SHA256   string `json:"sha256"`
		Stderr   string `json:"stderr,omitempty"`
		PeakRSSk int64  `json:"peakRssKB,omitempty"`
	}
	var lanes []lane
	for i := 0; i < 2; i++ {
		t := time.Now()
		res, err := c.Run(context.Background(), args, in)
		ms := time.Since(t).Milliseconds()
		l := lane{Run: i, MS: ms, RC: res.ExitCode}
		if err != nil {
			l.Stderr = err.Error()
		} else {
			obj := res.OutFiles["/work/out.o"]
			sum := sha256.Sum256(obj)
			l.Bytes = len(obj)
			l.SHA256 = hex.EncodeToString(sum[:])
			if s := strings.TrimSpace(string(res.Stderr)); s != "" {
				if len(s) > 1500 {
					s = s[:1500]
				}
				l.Stderr = s
			}
			if i == 1 {
				_ = os.WriteFile("/out/wasmedge.o", obj, 0o644)
			}
		}
		lanes = append(lanes, l)
	}
	out := map[string]any{"loadMs": loadMs, "aot": c.AOTEnabled(), "lanes": lanes}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
}

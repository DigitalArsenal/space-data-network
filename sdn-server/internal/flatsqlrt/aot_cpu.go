package flatsqlrt

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"reflect"
	"runtime"
	"sort"
	"strings"

	"golang.org/x/sys/cpu"
)

// An AOT artifact is NATIVE CODE FOR THE CPU THAT COMPILED IT. WasmEdge's AOT
// compiler targets the host, so a machine with AVX-512 produces AVX-512
// instructions, and running that artifact on a CPU without them is an illegal
// instruction — not a slow path, a dead process.
//
// Even two CPUs at the same x86-64 psABI level (e.g. two x86-64-v4 runners
// where one has AVX512-FP16 / VMOVW and the other does not, stack issue 815)
// emit incompatible AOT instructions. Including both the microarchitecture
// level and a short digest of the host's actual CPU feature flags turns a
// mismatched cache into a safe cache MISS instead of a SIGILL crash.

// aotCPUProfile identifies the instruction set an artifact was compiled for:
// the microarchitecture level plus an 8-hex digest of the host's CPU feature
// flags.
func aotCPUProfile() string {
	return aotCPUProfileFor(runtime.GOARCH, hostCPUFeatureFlags())
}

func aotCPUProfileFor(goarch string, flags []string) string {
	base := aotCPUBaseLevel(goarch, flags)
	normalized := normalizeCPUFlags(flags)
	if len(normalized) == 0 {
		return base
	}
	sum := sha256.Sum256([]byte(strings.Join(normalized, " ")))
	return base + "-" + hex.EncodeToString(sum[:4])
}

func aotCPUBaseLevel(goarch string, flags []string) string {
	if goarch != "amd64" {
		return goarch
	}
	has := make(map[string]bool, len(flags))
	for _, f := range flags {
		has[strings.ToLower(strings.TrimSpace(f))] = true
	}
	switch {
	case has["avx512f"] && has["avx512bw"] && has["avx512cd"] && has["avx512dq"] && has["avx512vl"]:
		return "x86-64-v4"
	case has["avx2"] && has["bmi1"] && has["bmi2"] && has["fma"] && has["avx"]:
		return "x86-64-v3"
	case has["sse41"] && has["sse42"] && has["popcnt"]:
		return "x86-64-v2"
	default:
		return "x86-64-v1"
	}
}

func hostCPUFeatureFlags() []string {
	var flags []string
	switch runtime.GOARCH {
	case "amd64", "386":
		flags = append(flags, boolStructFlags(cpu.X86)...)
	case "arm64":
		flags = append(flags, boolStructFlags(cpu.ARM64)...)
	case "arm":
		flags = append(flags, boolStructFlags(cpu.ARM)...)
	}
	// On Linux, /proc/cpuinfo exposes extensions such as avx512_fp16 that
	// golang.org/x/sys/cpu may not yet name individually (stack issue 815).
	if raw, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		flags = append(flags, parseProcCPUInfoFlags(string(raw))...)
	}
	return normalizeCPUFlags(flags)
}

func boolStructFlags(v any) []string {
	rv := reflect.ValueOf(v)
	rt := rv.Type()
	var out []string
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if !f.IsExported() || f.Type.Kind() != reflect.Bool || !strings.HasPrefix(f.Name, "Has") {
			continue
		}
		if rv.Field(i).Bool() {
			out = append(out, strings.ToLower(strings.TrimPrefix(f.Name, "Has")))
		}
	}
	return out
}

func parseProcCPUInfoFlags(content string) []string {
	for _, line := range strings.Split(content, "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "flags", "features":
			return strings.Fields(strings.ToLower(val))
		}
	}
	return nil
}

func normalizeCPUFlags(flags []string) []string {
	seen := make(map[string]struct{}, len(flags))
	out := make([]string, 0, len(flags))
	for _, f := range flags {
		f = strings.ToLower(strings.TrimSpace(f))
		if f == "" {
			continue
		}
		if _, dup := seen[f]; dup {
			continue
		}
		seen[f] = struct{}{}
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// sanitizeAOTKeyPart keeps a key component safe for a filename.
func sanitizeAOTKeyPart(s string) string {
	return strings.Map(func(r rune) rune {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			r == '.' || r == '-' {
			return r
		}
		return '_'
	}, s)
}

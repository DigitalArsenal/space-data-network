// Command genapprecord derives the canonical conjunction APP record (App 1)
// from the embedded self-contained serving artifact and writes it out. It is
// the build/release-time generator referenced by internal/appmanifest's C4
// decision: the record is never committed as a second copy of the artifact —
// it is generated on demand from cmd/spacedatanetwork/embedded/conjunction_app.html.
//
// Usage:
//
//	go run ./internal/appmanifest/genapprecord [-html PATH] [-format json|app] [-out PATH]
//
// -format json  emits deterministic canonical JSON (default).
// -format app   emits the size-prefixed $APP FlatBuffer bytes.
// -out  "-"     writes to stdout (default); otherwise a file path.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/spacedatanetwork/sdn-server/internal/appmanifest"
)

func defaultHTMLPath() string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	// internal/appmanifest/genapprecord/main.go -> repo sdn-server root.
	root := filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
	return filepath.Join(root, "cmd", "spacedatanetwork", "embedded", "conjunction_app.html")
}

func main() {
	htmlPath := flag.String("html", defaultHTMLPath(), "path to the conjunction_app.html serving artifact")
	format := flag.String("format", "json", "output format: json (canonical JSON) or app (size-prefixed $APP FlatBuffer)")
	out := flag.String("out", "-", "output path, or - for stdout")
	flag.Parse()

	if *htmlPath == "" {
		fmt.Fprintln(os.Stderr, "genapprecord: no -html path and could not locate the default artifact")
		os.Exit(1)
	}

	htmlBytes, err := os.ReadFile(*htmlPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "genapprecord: read %s: %v\n", *htmlPath, err)
		os.Exit(1)
	}

	manifest, err := appmanifest.NewConjunctionApp(htmlBytes)
	if err != nil {
		fmt.Fprintf(os.Stderr, "genapprecord: build record: %v\n", err)
		os.Exit(1)
	}

	var payload []byte
	switch *format {
	case "json":
		payload, err = manifest.MarshalCanonicalJSON()
	case "app":
		payload, err = manifest.ToAPP()
	default:
		fmt.Fprintf(os.Stderr, "genapprecord: unknown -format %q (want json or app)\n", *format)
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "genapprecord: serialize (%s): %v\n", *format, err)
		os.Exit(1)
	}

	if *out == "-" {
		if _, err := os.Stdout.Write(payload); err != nil {
			fmt.Fprintf(os.Stderr, "genapprecord: write stdout: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if err := os.WriteFile(*out, payload, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "genapprecord: write %s: %v\n", *out, err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "genapprecord: wrote %d bytes (%s) to %s\n", len(payload), *format, *out)
}

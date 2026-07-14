// Command genapprecord derives a canonical SDN APP record from the checked-in
// self-contained serving artifact and writes it out. It is the build/release-time
// generator referenced by internal/appmanifest's C4 decision: a record is never
// committed as a second copy of its artifact — it is generated on demand from the
// single source of truth.
//
// Two apps are supported (-app):
//
//	conjunction      App 1 — from cmd/spacedatanetwork/embedded/conjunction_app.html
//	                 (default, unchanged from C4).
//	supplemental-omm App 2 — from internal/appmanifest/embedded/supplemental_omm_board.html.
//
// Usage:
//
//	go run ./internal/appmanifest/genapprecord [-app NAME] [-html PATH] [-format json|app] [-out PATH]
//
// -format json  emits deterministic canonical JSON (default).
// -format app   emits the size-prefixed $APP FlatBuffer bytes.
// -out  "-"     writes to stdout (default); otherwise a file path.
// -html PATH    overrides the default artifact path for the chosen -app.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/spacedatanetwork/sdn-server/internal/appmanifest"
)

// sdnServerRoot returns the sdn-server repo root relative to this source file
// (internal/appmanifest/genapprecord/main.go -> up three dirs).
func sdnServerRoot() string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
}

// appSpec describes one buildable app: where its default artifact lives and how
// to turn the artifact bytes into a canonical AppManifest.
type appSpec struct {
	defaultHTML func() string
	build       func([]byte) (*appmanifest.AppManifest, error)
}

func appSpecs() map[string]appSpec {
	root := sdnServerRoot()
	return map[string]appSpec{
		"conjunction": {
			defaultHTML: func() string {
				if root == "" {
					return ""
				}
				return filepath.Join(root, "cmd", "spacedatanetwork", "embedded", "conjunction_app.html")
			},
			build: appmanifest.NewConjunctionApp,
		},
		"supplemental-omm": {
			defaultHTML: func() string {
				if root == "" {
					return ""
				}
				return filepath.Join(root, "internal", "appmanifest", "embedded", "supplemental_omm_board.html")
			},
			build: appmanifest.NewSupplementalOMMApp,
		},
	}
}

func main() {
	app := flag.String("app", "conjunction", "which app record to build: conjunction or supplemental-omm")
	htmlPath := flag.String("html", "", "path to the serving artifact (default: the chosen app's checked-in artifact)")
	format := flag.String("format", "json", "output format: json (canonical JSON) or app (size-prefixed $APP FlatBuffer)")
	out := flag.String("out", "-", "output path, or - for stdout")
	flag.Parse()

	spec, ok := appSpecs()[*app]
	if !ok {
		fmt.Fprintf(os.Stderr, "genapprecord: unknown -app %q (want conjunction or supplemental-omm)\n", *app)
		os.Exit(1)
	}

	path := *htmlPath
	if path == "" {
		path = spec.defaultHTML()
	}
	if path == "" {
		fmt.Fprintln(os.Stderr, "genapprecord: no -html path and could not locate the default artifact")
		os.Exit(1)
	}

	htmlBytes, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "genapprecord: read %s: %v\n", path, err)
		os.Exit(1)
	}

	manifest, err := spec.build(htmlBytes)
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
	fmt.Fprintf(os.Stderr, "genapprecord: wrote %d bytes (%s, app=%s) to %s\n", len(payload), *format, *app, *out)
}

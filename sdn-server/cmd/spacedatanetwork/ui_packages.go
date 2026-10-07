package main

// The UI this node serves: the dashboard and homepage built into the binary,
// or a UI package the update lane installed while the node ran
// (internal/update/uipackage.go; owner 2026-10-07: "have the running servers
// update in situ WITHOUT needing to republish the binaries").
//
// Every request looks at updates/ui/current.json (one stat) and loads the set
// again when it changed, so an install by the signal lane, a hand install and
// a hand rollback all take effect on the next request with nothing else to
// tell. A package serves only on a build it names; on any other build, and
// whenever a package cannot be read back exactly as it was verified, the node
// serves the UI built into its binary.

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/spacedatanetwork/sdn-server/internal/bundle"
	"github.com/spacedatanetwork/sdn-server/internal/update"
)

// uiSet is one complete UI.
type uiSet struct {
	// label names it in the X-SDN-UI header: "embedded" or the package version.
	label        string
	dashboard    []byte
	dashboardCSP string
	homepage     []byte
	homepageCSP  string
	// media are a package's own media files by name; /media/ falls back to
	// the binary's for any other name, so a page loaded before a switch still
	// finds what it asked for.
	media map[string][]byte
	// stamp is the current.json state this set was loaded for.
	stamp string
}

var embeddedUI = sync.OnceValue(func() *uiSet {
	return &uiSet{
		label:        "embedded",
		dashboard:    dashboardHTML,
		dashboardCSP: dashboardCSP(),
		homepage:     homepageHTML,
		homepageCSP:  strings.TrimSpace(homepageCSPRaw),
	}
})

// uiSource picks the served UI. paths is zero outside a self-contained
// bundle, where only the embedded UI exists.
type uiSource struct {
	once    sync.Once
	paths   update.Paths
	running string

	mu       sync.Mutex
	active   atomic.Pointer[uiSet]
	onChange func(*uiSet)
}

var servedUI = &uiSource{}

func (s *uiSource) init() {
	s.once.Do(func() {
		if s.paths.Updates != "" {
			return
		}
		if layout := bundle.ResolveCurrent(); layout.Root != "" {
			s.paths = update.PathsFor(layout.Root)
			s.running = update.RunningIdentity().BundleVersion
		}
	})
}

// OnChange registers fn for every switch of the served UI, and calls it once
// with the UI served now.
func (s *uiSource) OnChange(fn func(*uiSet)) {
	s.current()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onChange = fn
	if set := s.active.Load(); set != nil {
		fn(set)
		return
	}
	fn(embeddedUI())
}

func (s *uiSource) current() *uiSet {
	s.init()
	if s.paths.Updates == "" {
		return embeddedUI()
	}
	stamp := ""
	if info, err := os.Stat(update.UICurrentPath(s.paths)); err == nil {
		stamp = fmt.Sprintf("%d/%d", info.ModTime().UnixNano(), info.Size())
	}
	if set := s.active.Load(); set != nil && set.stamp == stamp {
		return set
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if set := s.active.Load(); set != nil && set.stamp == stamp {
		return set
	}
	set := s.load(stamp)
	s.active.Store(set)
	if s.onChange != nil {
		s.onChange(set)
	}
	return set
}

func (s *uiSource) load(stamp string) *uiSet {
	fallback := *embeddedUI()
	fallback.stamp = stamp
	pkg, err := update.ActiveUIPackage(s.paths)
	if err != nil {
		log.Warnf("UI package not served: %v. Serving the UI built into this binary.", err)
		return &fallback
	}
	if pkg == nil {
		return &fallback
	}
	if !pkg.ServesOn(s.running) {
		log.Infof("UI package %s was made for builds %s and this node runs %q, so it serves the UI built into this binary.",
			pkg.Version, strings.Join(pkg.BundleVersions, ", "), s.running)
		return &fallback
	}
	set := &uiSet{label: pkg.Version, stamp: stamp, media: map[string][]byte{}}
	files := map[string]*[]byte{"dashboard.html": &set.dashboard, "homepage.html": &set.homepage}
	policies := map[string]*string{"dashboard.csp": &set.dashboardCSP, "homepage.csp": &set.homepageCSP}
	for rel := range pkg.Files {
		data, err := pkg.ReadFile(s.paths, rel)
		if err != nil {
			log.Warnf("UI package %s not served: %v. Serving the UI built into this binary.", pkg.Version, err)
			return &fallback
		}
		switch {
		case files[rel] != nil:
			*files[rel] = data
		case policies[rel] != nil:
			*policies[rel] = strings.TrimSpace(string(data))
		case strings.HasPrefix(rel, "media/"):
			set.media[strings.TrimPrefix(rel, "media/")] = data
		}
	}
	if len(set.dashboard) == 0 || set.dashboardCSP == "" || len(set.homepage) == 0 || set.homepageCSP == "" {
		log.Warnf("UI package %s is incomplete. Serving the UI built into this binary.", pkg.Version)
		return &fallback
	}
	log.Infof("Serving UI package %s (sequence %d) in place of the UI built into this binary.", pkg.Version, pkg.Sequence)
	return set
}

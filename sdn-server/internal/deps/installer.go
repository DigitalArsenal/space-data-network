package deps

import (
	"errors"
	"fmt"
	"strings"
)

// InstallFunc pulls and registers one module (identified by plugin id + version),
// returning an error to abort the install. It is where the Go node runs the
// grant/pull (deliveryclient) + module load + plugins.Manager.Register, and the
// browser/Helia node runs its equivalent. The installer calls it once per module
// actually installed, dependency-first.
type InstallFunc func(step PlanStep) error

// Install resolves root's transitive dependency closure and installs every
// not-yet-installed module dependency-first, then the root itself. This is the
// decentralized package manager's install algorithm: installing A recursively
// pulls and registers everything A needs, in an order where each module's
// dependencies are installed before it.
//
// installFn is invoked once per module actually installed (already-installed
// modules are skipped). Install returns the ordered list of modules it installed
// (dependencies first, root last). On the first installFn error it stops and
// returns what was installed so far plus the error.
func Install(root Manifest, installed InstalledSet, catalog Catalog, installFn InstallFunc) ([]PlanStep, error) {
	if installFn == nil {
		return nil, errors.New("deps: install func is required")
	}

	closure, err := ResolveClosure(root, installed, catalog)
	if err != nil {
		return nil, err
	}

	steps := make([]PlanStep, 0, len(closure)+1)
	steps = append(steps, closure...)

	// The root installs last, after its dependency closure — unless it is
	// already installed (a repair/refresh of an existing module's deps).
	if rootID := strings.TrimSpace(root.PluginID); rootID != "" && !isInstalled(installed, rootID) {
		steps = append(steps, PlanStep{PluginID: rootID, Version: root.Version})
	}

	done := make([]PlanStep, 0, len(steps))
	for _, step := range steps {
		if err := installFn(step); err != nil {
			return done, fmt.Errorf("install %s@%s: %w", step.PluginID, step.Version, err)
		}
		done = append(done, step)
	}
	return done, nil
}

func isInstalled(installed InstalledSet, pluginID string) bool {
	if installed == nil {
		return false
	}
	_, ok := installed.InstalledVersion(pluginID)
	return ok
}

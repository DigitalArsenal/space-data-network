// Package sdncron is the SDN node cron scheduler + per-module configuration
// store. It is the foundation for user-configurable, self-scheduling WASM
// modules: every registered module declares timers (manifest Timers, surfaced
// as plugins.CronMethodSpec), and the scheduler fires each timer's cron method
// on its effective interval via a live ticker. The interval is resolved from a
// per-module configuration object persisted under the node home directory
// (overriding the manifest default), and can be changed at runtime from two
// paths that both re-schedule the live ticker:
//
//   - a running module's schedule_cron host capability (CronCapHandler), and
//   - the operator settings API (Scheduler.ApplyConfig, PUT
//     /sdn/v1/modules/{id}/config).
//
// The schedule_cron capability stays fail-closed: it is classified sensitive
// (modulert.sensitiveCapabilities), so a module may self-reschedule only after
// an operator records an approval keyed by the module's content hash.
//
// # Home-directory config layout
//
// Each module's configuration is one JSON file under the node's SDN config
// root: <repo>/sdn/modules/<moduleId>.json (0600, atomic writes). The module
// id is sanitized to a filesystem-safe name. The file is an arbitrary JSON
// object so a module can carry any user-tunable inputs; the scheduler
// interprets two RESERVED keys and leaves everything else opaque:
//
//	interval_ms  (number > 0)  overrides the firing interval for EVERY timer
//	                           the module declares.
//	timers       (object)      per-timer override, methodId -> interval ms
//	                           (number > 0); takes precedence over interval_ms
//	                           for that method.
package sdncron

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Reserved configuration keys the scheduler interprets. All other keys are
// opaque module inputs, preserved verbatim across read/write.
const (
	configKeyIntervalMs = "interval_ms"
	configKeyTimers     = "timers"
	// configKeyTimerInput is an OPTIONAL object the scheduler passes as the
	// invoke-request payload to a module's timer method on every scheduled fire
	// (it is nil when unset, preserving the prior no-payload behaviour). It is the
	// config-driven seam for a data-source module's pull config — e.g.
	// {"timer_input":{"objectCap":100000}} raises the per-pull object cap so a
	// provider ingests its full constellation, not the module's built-in default —
	// and is editable from the Modules settings API like any other config key.
	configKeyTimerInput = "timer_input"
)

// ModuleConfig is a per-module configuration object (see the package doc for
// the reserved keys). It is an arbitrary JSON object; unknown keys round-trip
// untouched.
type ModuleConfig map[string]interface{}

// Validate checks that the reserved schedule keys, when present, are
// well-formed positive integer millisecond intervals. Unknown keys are not
// constrained.
func (c ModuleConfig) Validate() error {
	if c == nil {
		return nil
	}
	if v, ok := c[configKeyIntervalMs]; ok {
		if _, ok := asPositiveInt64(v); !ok {
			return fmt.Errorf("%q must be a positive integer (milliseconds)", configKeyIntervalMs)
		}
	}
	if v, ok := c[configKeyTimers]; ok {
		m, ok := v.(map[string]interface{})
		if !ok {
			return fmt.Errorf("%q must be an object of methodId -> interval milliseconds", configKeyTimers)
		}
		for method, mv := range m {
			if _, ok := asPositiveInt64(mv); !ok {
				return fmt.Errorf("%q.%q must be a positive integer (milliseconds)", configKeyTimers, method)
			}
		}
	}
	if v, ok := c[configKeyTimerInput]; ok {
		if _, ok := v.(map[string]interface{}); !ok {
			return fmt.Errorf("%q must be a JSON object of timer-invoke input fields", configKeyTimerInput)
		}
	}
	return nil
}

// timerInput returns the scheduled-invoke input payload for a fired timer: the
// JSON encoding of the reserved timer_input object, or nil when it is unset or
// empty (nil preserves the historical no-payload fire). A module's timer method
// receives these bytes as its invoke request (e.g. a data-source pull config).
func (c ModuleConfig) timerInput() []byte {
	if c == nil {
		return nil
	}
	obj, ok := c[configKeyTimerInput].(map[string]interface{})
	if !ok || len(obj) == 0 {
		return nil
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return nil
	}
	return b
}

// intervalMs returns the module-wide interval override (reserved key
// interval_ms), when present and valid.
func (c ModuleConfig) intervalMs() (int64, bool) {
	if c == nil {
		return 0, false
	}
	return asPositiveInt64(c[configKeyIntervalMs])
}

// timerIntervalMs returns the per-timer interval override for method, when
// present and valid.
func (c ModuleConfig) timerIntervalMs(method string) (int64, bool) {
	if c == nil {
		return 0, false
	}
	m, ok := c[configKeyTimers].(map[string]interface{})
	if !ok {
		return 0, false
	}
	return asPositiveInt64(m[method])
}

// asPositiveInt64 coerces a decoded JSON number (float64 or json.Number) or a
// Go integer into a positive int64, rejecting non-integers and non-positives.
func asPositiveInt64(v interface{}) (int64, bool) {
	switch t := v.(type) {
	case float64:
		if t <= 0 || t != math.Trunc(t) || t > math.MaxInt64 {
			return 0, false
		}
		return int64(t), true
	case json.Number:
		i, err := t.Int64()
		if err != nil || i <= 0 {
			return 0, false
		}
		return i, true
	case int:
		if t <= 0 {
			return 0, false
		}
		return int64(t), true
	case int64:
		if t <= 0 {
			return 0, false
		}
		return t, true
	default:
		return 0, false
	}
}

// cloneConfig returns a deep copy via a JSON round-trip, so callers cannot
// mutate a scheduler's cached config and number types normalize to float64.
func cloneConfig(c ModuleConfig) ModuleConfig {
	if c == nil {
		return nil
	}
	b, err := json.Marshal(map[string]interface{}(c))
	if err != nil {
		return ModuleConfig{}
	}
	var out ModuleConfig
	if err := json.Unmarshal(b, &out); err != nil {
		return ModuleConfig{}
	}
	if out == nil {
		out = ModuleConfig{}
	}
	return out
}

// ConfigStore persists each module's ModuleConfig as one JSON file under dir
// (the node's <repo>/sdn/modules directory). An empty dir puts the store in
// no-persistence mode: Load returns an empty config and Save is a no-op, so a
// node with no repo path still runs (schedules just do not survive a restart).
type ConfigStore struct {
	dir string
	mu  sync.Mutex
}

// NewConfigStore opens (creating if needed) the module config directory. An
// empty dir selects no-persistence mode.
func NewConfigStore(dir string) (*ConfigStore, error) {
	dir = strings.TrimSpace(dir)
	if dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("sdncron: create module config dir %q: %w", dir, err)
		}
	}
	return &ConfigStore{dir: dir}, nil
}

// Dir returns the module config directory ("" in no-persistence mode).
func (s *ConfigStore) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
}

// Path returns the on-disk config path for moduleID ("" in no-persistence mode
// or for an unsafe id).
func (s *ConfigStore) Path(moduleID string) string {
	if s == nil || s.dir == "" {
		return ""
	}
	name, err := safeName(moduleID)
	if err != nil {
		return ""
	}
	return filepath.Join(s.dir, name+".json")
}

// Load reads moduleID's persisted config. A missing file is not an error: it
// yields an empty config (the module runs on its manifest defaults).
func (s *ConfigStore) Load(moduleID string) (ModuleConfig, error) {
	if s == nil || s.dir == "" {
		return ModuleConfig{}, nil
	}
	name, err := safeName(moduleID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(filepath.Join(s.dir, name+".json"))
	if err != nil {
		if os.IsNotExist(err) {
			return ModuleConfig{}, nil
		}
		return nil, fmt.Errorf("sdncron: read config %q: %w", moduleID, err)
	}
	var cfg ModuleConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("sdncron: parse config %q: %w", moduleID, err)
	}
	if cfg == nil {
		cfg = ModuleConfig{}
	}
	return cfg, nil
}

// Save persists moduleID's config atomically (temp file + rename) with 0600
// permissions. It is a no-op in no-persistence mode.
func (s *ConfigStore) Save(moduleID string, cfg ModuleConfig) error {
	if s == nil || s.dir == "" {
		return nil
	}
	name, err := safeName(moduleID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("sdncron: create module config dir: %w", err)
	}
	data, err := json.MarshalIndent(map[string]interface{}(cfg), "", "  ")
	if err != nil {
		return fmt.Errorf("sdncron: encode config %q: %w", moduleID, err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(s.dir, "."+name+".*.tmp")
	if err != nil {
		return fmt.Errorf("sdncron: temp config %q: %w", moduleID, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("sdncron: write config %q: %w", moduleID, err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("sdncron: chmod config %q: %w", moduleID, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("sdncron: close config %q: %w", moduleID, err)
	}
	if err := os.Rename(tmpPath, filepath.Join(s.dir, name+".json")); err != nil {
		return fmt.Errorf("sdncron: commit config %q: %w", moduleID, err)
	}
	return nil
}

// safeName maps a module id to a filesystem-safe base name, replacing any
// character outside [A-Za-z0-9._-] with '_' so a hostile or exotic module id
// can never escape the config directory.
func safeName(moduleID string) (string, error) {
	id := strings.TrimSpace(moduleID)
	if id == "" {
		return "", errors.New("sdncron: empty module id")
	}
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	name := b.String()
	if name == "" || name == "." || name == ".." {
		return "", fmt.Errorf("sdncron: unsafe module id %q", moduleID)
	}
	return name, nil
}

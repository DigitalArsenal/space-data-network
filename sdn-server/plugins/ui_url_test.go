package plugins

import (
	"context"
	"net/http"
	"testing"
)

// fakeUIURLPlugin is a minimal Plugin that also implements UIProvider and
// UIURLSetter — the shape internal/modulert.Module has, and the shape
// Manager.SetModuleUIURL type-asserts against.
type fakeUIURLPlugin struct {
	id  string
	url string
}

func (p *fakeUIURLPlugin) ID() string                                  { return p.id }
func (p *fakeUIURLPlugin) Start(context.Context, RuntimeContext) error { return nil }
func (p *fakeUIURLPlugin) RegisterRoutes(*http.ServeMux)               {}
func (p *fakeUIURLPlugin) Close() error                                { return nil }

func (p *fakeUIURLPlugin) UIDescriptor() UIDescriptor {
	return UIDescriptor{
		Title: "Fake UI Plugin",
		URL:   p.url,
	}
}

func (p *fakeUIURLPlugin) SetUIURL(url string) {
	p.url = url
}

// fakeNoUISetterPlugin implements Plugin + UIProvider but not UIURLSetter —
// a Go-native plugin (like plugins/ailogplugin) that hardcodes its own UI
// URL and has no need to accept one from an app manifest.
type fakeNoUISetterPlugin struct {
	id string
}

func (p *fakeNoUISetterPlugin) ID() string                                  { return p.id }
func (p *fakeNoUISetterPlugin) Start(context.Context, RuntimeContext) error { return nil }
func (p *fakeNoUISetterPlugin) RegisterRoutes(*http.ServeMux)               {}
func (p *fakeNoUISetterPlugin) Close() error                                { return nil }

func (p *fakeNoUISetterPlugin) UIDescriptor() UIDescriptor {
	return UIDescriptor{Title: "Fake No-Setter Plugin", URL: "/builtin/path"}
}

func TestSetModuleUIURLPopulatesUIDescriptorURL(t *testing.T) {
	mgr := New()
	plugin := &fakeUIURLPlugin{id: "spaceaware-ui"}
	if err := mgr.Register(plugin); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Before assignment, the module reports an empty UI URL — the
	// pre-H1/back-compat state a module with no app-manifest UI entry
	// stays in forever.
	entries := mgr.Manifest()
	if len(entries) != 1 || entries[0].UI == nil || entries[0].UI.URL != "" {
		t.Fatalf("manifest before SetModuleUIURL = %#v, want empty UI URL", entries)
	}

	if err := mgr.SetModuleUIURL("spaceaware-ui", "/apps/spaceaware-console/"); err != nil {
		t.Fatalf("SetModuleUIURL failed: %v", err)
	}

	entries = mgr.Manifest()
	if len(entries) != 1 || entries[0].UI == nil {
		t.Fatalf("manifest after SetModuleUIURL = %#v, want a UI descriptor", entries)
	}
	if got, want := entries[0].UI.URL, "/apps/spaceaware-console/"; got != want {
		t.Fatalf("manifest UI.URL = %q, want %q", got, want)
	}

	snapshot := mgr.RuntimeSnapshot()
	if snapshot.Count != 1 || snapshot.Modules[0].UI == nil || snapshot.Modules[0].UI.URL != "/apps/spaceaware-console/" {
		t.Fatalf("runtime snapshot UI = %#v, want the assigned URL", snapshot.Modules[0].UI)
	}
}

func TestSetModuleUIURLErrorsForUnknownModule(t *testing.T) {
	mgr := New()
	if err := mgr.SetModuleUIURL("does-not-exist", "/x"); err == nil {
		t.Fatalf("SetModuleUIURL for an unregistered module: want error, got nil")
	}
}

func TestSetModuleUIURLErrorsWhenPluginDoesNotSupportIt(t *testing.T) {
	mgr := New()
	plugin := &fakeNoUISetterPlugin{id: "ailog"}
	if err := mgr.Register(plugin); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	err := mgr.SetModuleUIURL("ailog", "/apps/whatever/")
	if err == nil {
		t.Fatalf("SetModuleUIURL on a plugin without UIURLSetter: want error, got nil")
	}

	// The plugin's own hardcoded URL is untouched.
	entries := mgr.Manifest()
	if len(entries) != 1 || entries[0].UI == nil || entries[0].UI.URL != "/builtin/path" {
		t.Fatalf("manifest = %#v, want the plugin's own hardcoded UI URL untouched", entries)
	}
}

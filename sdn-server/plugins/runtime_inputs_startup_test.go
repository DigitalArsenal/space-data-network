package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

type startupInputPlugin struct {
	*lateCronPlugin
	order    []string
	values   []RuntimeModuleInputValue
	applyErr error
}

func TestRuntimeScheduleOutlivesAPIRequestAndStopsWithManager(t *testing.T) {
	for _, action := range []string{"restart", "save-schedule"} {
		t.Run(action, func(t *testing.T) {
			manager := New()
			t.Cleanup(func() { _ = manager.Close() })
			lifetime, stopLifetime := context.WithCancel(context.Background())
			defer stopLifetime()
			plugin := &lateCronPlugin{id: "request-lifetime-fixture", interval: "1s"}
			if err := manager.Register(plugin); err != nil {
				t.Fatal(err)
			}
			if err := manager.StartAll(lifetime, RuntimeContext{BaseDataPath: t.TempDir()}); err != nil {
				t.Fatal(err)
			}
			request, finishRequest := context.WithCancel(lifetime)
			defer finishRequest()
			if action == "restart" {
				if err := manager.RunRuntimeModuleAction(request, plugin.ID(), "restart"); err != nil {
					t.Fatal(err)
				}
			} else if _, err := manager.SaveRuntimeModuleSchedule(request, plugin.ID(), "tick", RuntimeModuleScheduleConfig{Enabled: true, Interval: "1s"}); err != nil {
				t.Fatal(err)
			}
			before := plugin.ticks.Load()
			finishRequest() // the HTTP response has completed
			deadline := time.Now().Add(3 * time.Second)
			for plugin.ticks.Load() == before && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if plugin.ticks.Load() == before {
				t.Fatal("completing the API request stopped the persistent schedule")
			}
			stopLifetime()
			stopped := make(chan struct{})
			go func() {
				manager.cronWg.Wait()
				close(stopped)
			}()
			select {
			case <-stopped:
			case <-time.After(2 * time.Second):
				t.Fatal("schedule did not stop with the manager lifetime")
			}
		})
	}
}

func (p *startupInputPlugin) Start(context.Context, RuntimeContext) error {
	p.order = append(p.order, "start")
	return nil
}
func (p *startupInputPlugin) ApplyRuntimeModuleInputs(_ context.Context, values []RuntimeModuleInputValue) error {
	p.order = append(p.order, "apply")
	p.values = cloneRuntimeModuleInputValues(values)
	return p.applyErr
}
func (p *startupInputPlugin) CronMethods() []CronMethodSpec {
	p.order = append(p.order, "cron")
	return p.lateCronPlugin.CronMethods()
}
func (p *startupInputPlugin) Close() error {
	p.order = append(p.order, "close")
	return nil
}

func writeStartupInputFixture(t *testing.T, dir string, ids ...string) []RuntimeModuleInputValue {
	t.Helper()
	values := []RuntimeModuleInputValue{{MethodID: "configure", PortID: "configuration", WireFormat: "json", Encoding: "utf8", Value: `{"enabled":true}`}}
	states := map[string]RuntimeModuleInputState{}
	for _, id := range ids {
		states[id] = RuntimeModuleInputState{Values: values, RestartPending: true}
	}
	data, err := json.Marshal(states)
	if err != nil {
		t.Fatal(err)
	}
	file := runtimeModuleInputStatePath(dir)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return values
}

func TestStartupAppliesPersistedInputsBeforeScheduling(t *testing.T) {
	for _, late := range []bool{false, true} {
		name := "registered"
		if late {
			name = "late"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			plugin := &startupInputPlugin{lateCronPlugin: &lateCronPlugin{id: "configured-fixture", interval: "1h"}}
			want := writeStartupInputFixture(t, dir, plugin.ID())
			manager := New()
			t.Cleanup(func() { _ = manager.Close() })
			if !late {
				if err := manager.Register(plugin); err != nil {
					t.Fatal(err)
				}
			}
			if err := manager.StartAll(context.Background(), RuntimeContext{BaseDataPath: dir}); err != nil {
				t.Fatal(err)
			}
			if late {
				if err := manager.Register(plugin); err != nil {
					t.Fatal(err)
				}
				if started, err := manager.StartLateRegistered(plugin); err != nil || !started {
					t.Fatalf("late startup: started=%v err=%v", started, err)
				}
			}
			if !reflect.DeepEqual(plugin.order, []string{"start", "apply", "cron"}) || !reflect.DeepEqual(plugin.values, want) {
				t.Fatalf("startup order/inputs: %v %+v", plugin.order, plugin.values)
			}
			if status, _ := manager.pluginStatus(plugin.ID()); status != "running" {
				t.Fatalf("configured plugin status=%s", status)
			}
			if manager.runtimeModuleInputState(plugin.ID()).RestartPending {
				t.Fatal("applied inputs remain pending")
			}
			// A second process still loads the saved values after the pending
			// marker is cleared; startup application is not conditional on it.
			second := New()
			second.loadRuntimeModuleInputState(dir)
			if state := second.runtimeModuleInputState(plugin.ID()); !reflect.DeepEqual(state.Values, want) || state.RestartPending {
				t.Fatalf("saved inputs changed after application: %+v", state)
			}
		})
	}
}

func TestStartupInputFailureClosesOnlyTheAffectedPlugin(t *testing.T) {
	dir := t.TempDir()
	failed := &startupInputPlugin{lateCronPlugin: &lateCronPlugin{id: "failed-fixture", interval: "1h"}, applyErr: errors.New("fixture input rejected")}
	healthy := &startupInputPlugin{lateCronPlugin: &lateCronPlugin{id: "healthy-fixture", interval: "1h"}}
	writeStartupInputFixture(t, dir, failed.ID(), healthy.ID())
	manager := New()
	t.Cleanup(func() { _ = manager.Close() })
	for _, plugin := range []Plugin{failed, healthy} {
		if err := manager.Register(plugin); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.StartAll(context.Background(), RuntimeContext{BaseDataPath: dir}); err == nil {
		t.Fatal("startup ignored input application failure")
	}
	if !reflect.DeepEqual(failed.order, []string{"start", "apply", "close"}) {
		t.Fatalf("failed plugin was scheduled or left open: %v", failed.order)
	}
	if !reflect.DeepEqual(healthy.order, []string{"start", "apply", "cron"}) {
		t.Fatalf("unrelated plugin did not start: %v", healthy.order)
	}
	if status, _ := manager.pluginStatus(failed.ID()); status != "error" {
		t.Fatalf("failed plugin status=%s", status)
	}
}

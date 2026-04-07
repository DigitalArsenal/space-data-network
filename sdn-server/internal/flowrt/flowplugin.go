package flowrt

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/plugins"
)

// FlowProgram holds the parsed flow definition metadata needed by the plugin adapter.
type FlowProgram struct {
	ProgramID   string        `json:"programId"`
	Name        string        `json:"name"`
	Version     string        `json:"version"`
	Description string        `json:"description"`
	Triggers    []FlowTrigger `json:"triggers"`
}

// FlowTrigger describes a trigger from the flow JSON.
type FlowTrigger struct {
	TriggerID        string `json:"triggerId"`
	Kind             string `json:"kind"`
	Source           string `json:"source"`
	DefaultIntervalMs int   `json:"defaultIntervalMs"`
	HTTPPath         string `json:"httpPath,omitempty"`
}

// FlowPlugin wraps a FlowRuntime to implement the SDN plugin manager interfaces.
// It maps flow triggers to SDN infrastructure: timer → cron, http → routes,
// pubsub → libp2p subscriptions.
type FlowPlugin struct {
	program  FlowProgram
	runtime  *FlowRuntime
	handlers HandlerMap
	wasmPath string

	mu       sync.Mutex
	cancel   context.CancelFunc
	stopped  bool
}

// NewFlowPlugin creates a FlowPlugin from a loaded runtime and its program definition.
func NewFlowPlugin(program FlowProgram, runtime *FlowRuntime, handlers HandlerMap, wasmPath string) *FlowPlugin {
	return &FlowPlugin{
		program:  program,
		runtime:  runtime,
		handlers: handlers,
		wasmPath: wasmPath,
	}
}

// --- plugins.Plugin interface ---

func (fp *FlowPlugin) ID() string { return fp.program.ProgramID }

func (fp *FlowPlugin) Start(ctx context.Context, runtime plugins.RuntimeContext) error {
	fp.mu.Lock()
	defer fp.mu.Unlock()

	ctx, cancel := context.WithCancel(ctx)
	fp.cancel = cancel
	fp.stopped = false

	log.Infof("Flow plugin %q started (%d triggers)", fp.program.ProgramID, len(fp.program.Triggers))
	return nil
}

func (fp *FlowPlugin) RegisterRoutes(mux *http.ServeMux) {
	for i, trigger := range fp.program.Triggers {
		if trigger.Kind != "http-request" || trigger.HTTPPath == "" {
			continue
		}

		trigIdx := uint32(i)
		path := trigger.HTTPPath
		log.Infof("Flow %q: registering HTTP trigger at %s (trigger %d)", fp.program.ProgramID, path, trigIdx)

		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			fp.handleHTTPTrigger(w, r, trigIdx)
		})
	}
}

func (fp *FlowPlugin) Close() error {
	fp.mu.Lock()
	defer fp.mu.Unlock()

	fp.stopped = true
	if fp.cancel != nil {
		fp.cancel()
	}
	if fp.runtime != nil {
		fp.runtime.Release()
		fp.runtime = nil
	}
	log.Infof("Flow plugin %q closed", fp.program.ProgramID)
	return nil
}

// --- plugins.CronProvider interface ---

func (fp *FlowPlugin) CronMethods() []plugins.CronMethodSpec {
	var specs []plugins.CronMethodSpec
	for _, trigger := range fp.program.Triggers {
		if trigger.Kind != "timer" {
			continue
		}
		interval := fmt.Sprintf("%dms", trigger.DefaultIntervalMs)
		if trigger.DefaultIntervalMs >= 1000 {
			interval = fmt.Sprintf("%ds", trigger.DefaultIntervalMs/1000)
		}
		specs = append(specs, plugins.CronMethodSpec{
			Method:          trigger.TriggerID,
			Description:     fmt.Sprintf("Timer trigger: %s", trigger.TriggerID),
			DefaultInterval: interval,
			Input:           "none",
			Output:          "none",
		})
	}
	return specs
}

func (fp *FlowPlugin) InvokeCron(ctx context.Context, method string, input []byte) ([]byte, error) {
	fp.mu.Lock()
	if fp.stopped || fp.runtime == nil {
		fp.mu.Unlock()
		return nil, fmt.Errorf("flow plugin %q is stopped", fp.program.ProgramID)
	}
	fp.mu.Unlock()

	// Find the trigger index by method (triggerId)
	for i, trigger := range fp.program.Triggers {
		if trigger.TriggerID == method {
			fp.runtime.EnqueueTrigger(uint32(i))
			result, err := fp.runtime.DrainOnce(ctx, fp.handlers)
			if err != nil {
				return nil, err
			}
			resp, _ := json.Marshal(result)
			return resp, nil
		}
	}
	return nil, fmt.Errorf("unknown cron method %q", method)
}

// --- plugins.UIProvider interface ---

func (fp *FlowPlugin) UIDescriptor() plugins.UIDescriptor {
	return plugins.UIDescriptor{
		Title:       fp.program.Name,
		Description: fp.program.Description,
		Icon:        "⚡",
		Color:       "#6366f1",
		TextColor:   "#ffffff",
	}
}

// --- HTTP trigger handler ---

func (fp *FlowPlugin) handleHTTPTrigger(w http.ResponseWriter, r *http.Request, triggerIndex uint32) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	fp.runtime.EnqueueTrigger(triggerIndex)
	result, err := fp.runtime.DrainOnce(ctx, fp.handlers)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// Runtime returns the underlying FlowRuntime.
func (fp *FlowPlugin) Runtime() *FlowRuntime { return fp.runtime }

// Program returns the flow program metadata.
func (fp *FlowPlugin) Program() FlowProgram { return fp.program }

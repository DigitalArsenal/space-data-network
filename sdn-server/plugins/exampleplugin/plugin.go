// Package exampleplugin demonstrates the PeriodicTaskProvider and
// StreamProvider optional interfaces for SDN plugins.
//
// This plugin:
//   - Registers two periodic tasks (heartbeat every 30s, cleanup every 5m)
//   - Registers a libp2p stream handler for a custom protocol
//   - Exposes an HTTP status endpoint
//
// Use this as a reference when building plugins that need background tasks
// or custom libp2p stream protocols.
package exampleplugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/spacedatanetwork/sdn-server/plugins"
)

const (
	// ID is the canonical plugin identifier.
	ID = "example-periodic"

	// SensorStreamProtocol is a custom libp2p protocol for streaming sensor data.
	SensorStreamProtocol = protocol.ID("/example/sensor-stream/1.0.0")
)

// Compile-time interface assertions.
var (
	_ plugins.Plugin               = (*Plugin)(nil)
	_ plugins.PeriodicTaskProvider = (*Plugin)(nil)
	_ plugins.StreamProvider       = (*Plugin)(nil)
)

// Plugin implements the SDN Plugin interface along with the optional
// PeriodicTaskProvider and StreamProvider interfaces.
type Plugin struct {
	heartbeatCount atomic.Int64
	lastCleanup    atomic.Int64
}

// New returns a new example plugin instance.
func New() *Plugin { return &Plugin{} }

func (p *Plugin) ID() string { return ID }

func (p *Plugin) Start(_ context.Context, _ plugins.RuntimeContext) error {
	return nil
}

func (p *Plugin) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/example-periodic/v1/status", p.handleStatus)
}

func (p *Plugin) Close() error { return nil }

func (p *Plugin) Version() string     { return "1.0.0" }
func (p *Plugin) Description() string { return "Example plugin with periodic tasks and streaming" }

// PeriodicTasks implements plugins.PeriodicTaskProvider.
//
// The plugin manager starts a goroutine per task after Start() completes.
// Each task runs on its own ticker. The context is cancelled when the
// manager shuts down — no manual goroutine management needed.
func (p *Plugin) PeriodicTasks() []plugins.PeriodicTaskConfig {
	return []plugins.PeriodicTaskConfig{
		{
			Name:       "heartbeat",
			Interval:   30 * time.Second,
			RunOnStart: true,
			Fn: func(ctx context.Context) error {
				count := p.heartbeatCount.Add(1)
				// Real plugin: publish health record, re-announce to DHT,
				// check peer connectivity, rotate ephemeral keys, etc.
				_ = count
				return nil
			},
		},
		{
			Name:       "cleanup",
			Interval:   5 * time.Minute,
			RunOnStart: false,
			Fn: func(ctx context.Context) error {
				p.lastCleanup.Store(time.Now().UnixMilli())
				// Real plugin: prune expired cache, compact local storage,
				// rotate log files, expire stale DHT records, etc.
				return nil
			},
		},
	}
}

// StreamHandlers implements plugins.StreamProvider.
//
// The plugin manager registers these on the libp2p host after Start()
// and removes them during Close(). Plugins don't need to call
// host.SetStreamHandler/RemoveStreamHandler themselves.
func (p *Plugin) StreamHandlers() []plugins.StreamHandlerConfig {
	return []plugins.StreamHandlerConfig{
		{
			ProtocolID: SensorStreamProtocol,
			Handler:    p.handleSensorStream,
		},
	}
}

// handleSensorStream processes incoming sensor data over a libp2p stream.
//
// Wire format: newline-delimited JSON, each line is a sensor reading.
// Response: JSON summary of processed records.
func (p *Plugin) handleSensorStream(stream network.Stream) {
	defer stream.Close()

	_ = stream.SetReadDeadline(time.Now().Add(15 * time.Second))
	data, err := io.ReadAll(io.LimitReader(stream, 64*1024))
	if err != nil {
		return
	}

	// Count newline-delimited JSON records.
	count := 0
	for _, b := range data {
		if b == '\n' {
			count++
		}
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		count++
	}

	response := fmt.Sprintf(`{"processed":%d,"timestamp":%d}`, count, time.Now().UnixMilli())
	_ = stream.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, _ = stream.Write([]byte(response))
}

func (p *Plugin) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"heartbeats":   p.heartbeatCount.Load(),
		"last_cleanup": p.lastCleanup.Load(),
	})
}

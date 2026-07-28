package config

import (
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// The node ships the CelesTrak retrieval flows as SERVICES, not as Go code.
// Declaring them is not the same as running them: LoadFlowServices skips a
// service whose bundle is not installed, so a node pulls from CelesTrak only
// after an operator deliberately installs the bundle.
func TestDefaultConfigDeclaresCelesTrakRetrievalFlows(t *testing.T) {
	cfg := Default()

	want := map[string]bool{
		CelesTrakGPIngestFlowID:           false,
		CelesTrakSatcatIngestFlowID:       false,
		CelesTrakSpaceWeatherIngestFlowID: false,
	}
	for _, service := range cfg.Flows.Services {
		if _, ok := want[service.Flow]; ok {
			want[service.Flow] = true
		}
	}
	for flow, found := range want {
		if !found {
			t.Fatalf("default config does not declare the %s flow service", flow)
		}
	}

	// The retrieval surface must stay in the flows; the Go ingest runner
	// carries CREDENTIALED sources only.
	if cfg.Ingest.SpaceTrackEnabled || cfg.Ingest.UDLEnabled {
		t.Fatal("default config must not enable credentialed ingest workers")
	}
}

// Egress spacing is host policy expressed as configuration. Parsing must be
// strict enough that a typo is visible rather than silently disabling pacing.
func TestEffectiveEgressMinIntervals(t *testing.T) {
	cfg := ModulesConfig{EgressMinInterval: map[string]string{
		"CelesTrak.org":  "5s",
		" example.com ":  "250ms",
		"broken.example": "not-a-duration",
		"negative.test":  "-1s",
		"":               "1s",
	}}
	intervals, invalid := cfg.EffectiveEgressMinIntervals()

	if got := intervals["celestrak.org"]; got != 5*time.Second {
		t.Fatalf("celestrak.org = %v, want 5s (host key lowercased)", got)
	}
	if got := intervals["example.com"]; got != 250*time.Millisecond {
		t.Fatalf("example.com = %v, want 250ms (host key trimmed)", got)
	}
	if _, ok := intervals["broken.example"]; ok {
		t.Fatal("an unparseable duration must not become an interval")
	}
	if len(invalid) != 2 {
		t.Fatalf("invalid entries = %v, want the unparseable and the negative one", invalid)
	}

	if intervals, invalid := (ModulesConfig{}).EffectiveEgressMinIntervals(); intervals != nil || invalid != nil {
		t.Fatalf("unset egress config produced %v / %v, want nil / nil", intervals, invalid)
	}
}

// The YAML surface an operator actually writes must round-trip into the same
// structures the node reads.
func TestCelesTrakRetrievalYAMLSurface(t *testing.T) {
	const doc = `
modules:
  scheduled_invoke_timeout: 20m
  egress_min_interval:
    celestrak.org: 5s
flows:
  enabled: true
  services:
    - flow: com.digitalarsenal.flows.celestrak-gp-ingest
      intervals:
        timer-gp: 6h
      config:
        celestrak_provider_id: space-data-network-02
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(doc), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Modules.ScheduledInvokeTimeout != 20*time.Minute {
		t.Fatalf("scheduled_invoke_timeout = %v", cfg.Modules.ScheduledInvokeTimeout)
	}
	intervals, invalid := cfg.Modules.EffectiveEgressMinIntervals()
	if len(invalid) != 0 || intervals["celestrak.org"] != 5*time.Second {
		t.Fatalf("egress_min_interval = %v (invalid %v)", intervals, invalid)
	}
	if len(cfg.Flows.Services) != 1 {
		t.Fatalf("services = %+v", cfg.Flows.Services)
	}
	service := cfg.Flows.Services[0]
	if service.Flow != CelesTrakGPIngestFlowID {
		t.Fatalf("flow = %q", service.Flow)
	}
	if service.Intervals["timer-gp"] != "6h" {
		t.Fatalf("interval override lost: %v", service.Intervals)
	}
	if service.Config["celestrak_provider_id"] != "space-data-network-02" {
		t.Fatalf("node config lost: %v", service.Config)
	}
}

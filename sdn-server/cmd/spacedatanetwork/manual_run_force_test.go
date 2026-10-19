package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/plugins"
)

type forceInputTestPlugin struct {
	runtimeMutationTestPlugin
	input  string
	manual bool
}

func (p *forceInputTestPlugin) InvokeCron(ctx context.Context, _ string, input []byte) ([]byte, error) {
	p.input, p.manual = string(input), plugins.ManualRun(ctx)
	return nil, nil
}

func TestManualRunHTTPPassesForceInput(t *testing.T) {
	for _, body := range []string{`{"force":true}`, ``} {
		t.Run(body, func(t *testing.T) {
			mgr := plugins.New()
			p := &forceInputTestPlugin{runtimeMutationTestPlugin: runtimeMutationTestPlugin{id: "catalogfixture-provider"}}
			if err := mgr.Register(p); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/modules/runtime/catalogfixture-provider/schedules/sync_full_catalog/run", strings.NewReader(body))
			rec := httptest.NewRecorder()
			handleModuleRuntimeMutation(mgr)(rec, req)
			if rec.Code != http.StatusOK || p.input != body || !p.manual {
				t.Fatalf("status=%d body=%s input=%q manual=%v", rec.Code, rec.Body.String(), p.input, p.manual)
			}
		})
	}
}

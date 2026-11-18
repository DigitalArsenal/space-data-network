package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/plugins"
)

// moduleScheduleCancelWait bounds how long a cancel request waits for the
// cancelled run to end before it answers with the run still "running".
const moduleScheduleCancelWait = 30 * time.Second

// handleModuleScheduleRun serves /api/v1/modules/runtime/{module}/schedules/
// {schedule}/run, an operator's manual run of a schedule
// (plugins/manual_runs.go). The run belongs to the node: no request's end
// ends it.
//
//	POST            start the run (the body is its input, such as
//	                {"force":true}) and answer when it ends; a client that
//	                leaves does not end the run. A run of the schedule that
//	                is already running is joined ("joined": true).
//	POST ?detach=1  start (or join) the run and answer at once, 202, with
//	                its state ("running").
//	GET             the running manual run, or the last one; 404 when the
//	                schedule has none.
//	DELETE          cancel the running manual run and answer its state once
//	                it ended (at most 30 s later); 409 when none runs.
func handleModuleScheduleRun(w http.ResponseWriter, r *http.Request, mgr *plugins.Manager, moduleID, methodID string) {
	writeRun := func(status int, run plugins.RuntimeModuleScheduleRun) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(run)
	}
	switch r.Method {
	case http.MethodGet:
		run, err := mgr.RuntimeModuleScheduleRunState(moduleID, methodID)
		if errors.Is(err, plugins.ErrNoManualRun) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeRun(http.StatusOK, run)
	case http.MethodDelete:
		ctx, cancel := context.WithTimeout(r.Context(), moduleScheduleCancelWait)
		defer cancel()
		run, err := mgr.CancelRuntimeModuleScheduleRun(ctx, moduleID, methodID)
		if errors.Is(err, plugins.ErrNoManualRun) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeRun(http.StatusOK, run)
	case http.MethodPost:
		input, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			http.Error(w, "invalid schedule input: "+err.Error(), http.StatusBadRequest)
			return
		}
		if detach, _ := strconv.ParseBool(strings.TrimSpace(r.URL.Query().Get("detach"))); detach {
			run, _, err := mgr.StartRuntimeModuleScheduleRun(r.Context(), moduleID, methodID, input)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeRun(http.StatusAccepted, run)
			return
		}
		// The answer comes when the run ends, which may be after the admin
		// server's write timeout: this response has no write deadline.
		if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil {
			log.Warnf("manual run %s %s: the response keeps the admin write timeout: %v", moduleID, methodID, err)
		}
		run, err := mgr.RunRuntimeModuleScheduleNowWithInput(r.Context(), moduleID, methodID, input)
		if r.Context().Err() != nil {
			// The client left; the run continues and no one reads an answer.
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeRun(http.StatusOK, run)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

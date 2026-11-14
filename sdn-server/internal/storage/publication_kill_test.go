package storage

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPublicationReclamationKillChild(t *testing.T) {
	dir := os.Getenv("SDN_RECLAIM_KILL_DIR")
	if dir == "" {
		t.Skip("subprocess only")
	}
	stage := os.Getenv("SDN_RECLAIM_KILL_STAGE")
	s := journalTestStore(t, dir)
	ready := func() {
		rows, err := s.QueryIndexedRecords(IndexedRecordQuery{SchemaName: "CAT.fbs", ProviderID: "synthetic", SourceName: "index"})
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, r := range rows {
			ids = append(ids, r.CID)
		}
		raw, _ := marshalPublication(ids)
		if err = os.WriteFile(filepath.Join(dir, "ready"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		select {}
	}
	if stage == "outcome-persisted" {
		s.publicationHook = func(at string) {
			if at == stage {
				ready()
			}
		}
		journalTestPut(t, s, "after")
	} else {
		s.publicationFault = func(at string) error {
			if at == stage {
				ready()
			}
			return nil
		}
		terminalPublish(t, s)
	}
	t.Fatal("boundary not reached")
}
func TestPublicationDurableKillBoundaries(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	for _, stage := range []string{"reclaim-receipt", "reclaim-renamed", "outcome-persisted"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			s := journalTestStore(t, dir)
			if err := s.ConfigurePublicationSources([]PublicationSource{{Schema: "CAT.fbs", Provider: "synthetic", Source: "index"}}); err != nil {
				t.Fatal(err)
			}
			journalTestPut(t, s, "before")
			journalTestPut(t, s, "latest")
			s.Close()
			cmd := exec.Command(os.Args[0], "-test.run=^TestPublicationReclamationKillChild$", "-test.timeout=3m")
			cmd.Env = append(os.Environ(), "SDN_RECLAIM_KILL_DIR="+dir, "SDN_RECLAIM_KILL_STAGE="+stage)
			log, err := os.Create(filepath.Join(dir, "child.log"))
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			cmd.Stdout = log
			cmd.Stderr = log
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			ready := filepath.Join(dir, "ready")
			deadline := time.Now().Add(2 * time.Minute)
			for {
				if _, err = os.Stat(ready); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("child did not reach boundary")
				}
				time.Sleep(20 * time.Millisecond)
			}
			// Inspect the exact child command before killing that PID.
			command, err := exec.Command("ps", "-p", fmt.Sprint(cmd.Process.Pid), "-o", "command=").Output()
			if err != nil || !strings.Contains(string(command), "TestPublicationReclamationKillChild") {
				t.Fatalf("unexpected child command: %v", err)
			}
			if err = cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err = cmd.Wait(); err == nil {
				t.Fatal("child was not killed")
			}
			raw, err := os.ReadFile(ready)
			if err != nil {
				t.Fatal(err)
			}
			var before []string
			if err = unmarshalPublication(raw, &before); err != nil {
				t.Fatal(err)
			}
			s = journalTestStore(t, dir)
			defer s.Close()
			rows, err := s.QueryIndexedRecords(IndexedRecordQuery{SchemaName: "CAT.fbs", ProviderID: "synthetic", SourceName: "index"})
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != len(before) {
				t.Fatal("live records changed")
			}
			for i, r := range rows {
				if r.CID != before[i] {
					t.Fatal("recovery replayed completed engine write")
				}
			}
			if len(s.PublicationRecoveryStatus()) != 0 {
				t.Fatal("recovery remained paused")
			}
			if err = s.ReclaimPublication(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

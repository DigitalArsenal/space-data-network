package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/update"
)

// installIntoHome is `update install` when updates go to a home bundle
// (internal/update/trial.go): the update is installed as a trial while the
// daemon serves, the daemon is asked to stop for it and starts again through
// the installed path in its own process, and this command reports what the
// trial came to. No helper. With no daemon running, the build runs as a trial
// at the node's next start.
func installIntoHome(out, errOut io.Writer, paths update.Paths, staged *update.StagedUpdate, storeRoot string) error {
	cfg, _ := mustLoadResolved(configPath)
	if cfg == nil {
		cfg = config.Default()
	}
	admin := adminURL(cfg)
	running := localDaemonAvailable(admin)
	result, err := update.InstallHomeUpdate(paths, update.InstallHomeOptions{
		UpdateID: staged.UpdateID, StoreRoot: storeRoot, Trigger: "operator", AllowRollback: updateInstallAllowRollback,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "installed_update=%s\n", result.UpdateID)
	fmt.Fprintf(out, "version=%s\n", result.Version)
	fmt.Fprintf(out, "sequence=%d\n", result.Sequence)
	fmt.Fprintf(out, "update_root=%s\n", paths.Root)
	if !running {
		fmt.Fprintln(out, "next=start the node: it runs this build as a trial and keeps it once it answers ready")
		return nil
	}
	client := daemonLoopbackHTTPClient(10 * time.Second)
	token, err := generateUpdateControlToken()
	if err != nil {
		return err
	}
	if err := update.WriteControlToken(paths, token); err != nil {
		return err
	}
	shutdownURL, err := adminEndpointURL(admin, update.ControlShutdownPath)
	if err != nil {
		return err
	}
	answer, err := update.RequestShutdown(client, shutdownURL, paths.Root, token)
	if err != nil {
		return fmt.Errorf("the update is installed, and the running daemon did not accept the request to restart for it (it runs at the next start): %w", err)
	}
	fmt.Fprintf(out, "daemon_restart=requested pid=%d\n", answer.PID)

	expected, err := update.BundleIdentity(paths.Root)
	if err != nil {
		return err
	}
	timeout := installHealthTimeout(updateInstallHealthTimeout, cfg) + 10*time.Minute
	deadline := time.Now().Add(timeout)
	ctx := context.Background()
	for time.Now().Before(deadline) {
		if update.HasFailedUpdate(paths, result.UpdateID) {
			state, _ := update.LoadState(paths)
			restored := "-"
			if state != nil {
				restored = state.Version
			}
			if err := probeDaemonHealth(ctx, client, admin); err != nil {
				time.Sleep(time.Second)
				continue
			}
			fmt.Fprintf(errOut, "daemon_health=unhealthy update_id=%s\n", result.UpdateID)
			fmt.Fprintf(out, "rollback=applied restored_version=%s failed_path=%s\n", restored, filepath.Join(paths.Failed, result.UpdateID))
			return fmt.Errorf("daemon health failed after update; rolled back to %s", restored)
		}
		trial, _ := update.ReadTrial(paths)
		if trial == nil {
			if identity, err := probeDaemonGate(ctx, client, admin, expected, true); err == nil {
				fmt.Fprintln(out, "daemon_health=healthy")
				fmt.Fprintf(out, "daemon_identity=verified %s\n", identity)
				return nil
			}
		}
		time.Sleep(time.Second)
	}
	return errors.New("the daemon neither passed nor failed the trial of " + result.UpdateID + " within " + timeout.String())
}

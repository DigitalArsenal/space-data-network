package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRootHelpListsServiceManagementCommands(t *testing.T) {
	help := rootCmd.UsageString()
	for _, want := range []string{"start", "stop", "restart", "remove", "service"} {
		if !strings.Contains(help, want) {
			t.Fatalf("root help did not list %q:\n%s", want, help)
		}
	}
}

func TestRenderLaunchAgentPlistStartsDaemonWithConfig(t *testing.T) {
	spec := serviceSpec{
		Executable: "/Applications/SDN/bin/spacedatanetwork",
		ConfigPath: "/Users/tj/.spacedatanetwork/config.yaml",
		WorkingDir: "/Applications/SDN",
	}

	plist := renderLaunchAgentPlist(spec)

	for _, want := range []string{
		"<key>Label</key>",
		"<string>org.spacedatanetwork.daemon</string>",
		"<string>/Applications/SDN/bin/spacedatanetwork</string>",
		"<string>daemon</string>",
		"<string>--config</string>",
		"<string>/Users/tj/.spacedatanetwork/config.yaml</string>",
		"<key>RunAtLoad</key>",
		"<true/>",
		"<key>KeepAlive</key>",
		"<true/>",
	} {
		if !strings.Contains(plist, want) {
			t.Fatalf("launch agent plist missing %q:\n%s", want, plist)
		}
	}
}

func TestRenderSystemdUserUnitStartsDaemonWithRestartPolicy(t *testing.T) {
	spec := serviceSpec{
		Executable: "/opt/spacedatanetwork/bin/spacedatanetwork",
		ConfigPath: "/home/tj/.spacedatanetwork/config.yaml",
		WorkingDir: "/opt/spacedatanetwork",
	}

	unit := renderSystemdUserUnit(spec)

	for _, want := range []string{
		"[Unit]",
		"Description=Space Data Network daemon",
		"ExecStart=/opt/spacedatanetwork/bin/spacedatanetwork daemon --config /home/tj/.spacedatanetwork/config.yaml",
		"WorkingDirectory=/opt/spacedatanetwork",
		"Restart=always",
		"WantedBy=default.target",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("systemd unit missing %q:\n%s", want, unit)
		}
	}
}

func TestWindowsScheduledTaskCommandsUseCurrentUserDaemon(t *testing.T) {
	spec := serviceSpec{
		Executable: `C:\Program Files\SpaceDataNetwork\bin\spacedatanetwork.exe`,
		ConfigPath: `C:\Users\tj\.spacedatanetwork\config.yaml`,
		WorkingDir: `C:\Program Files\SpaceDataNetwork`,
	}

	commands := windowsServiceInstallCommands(spec)

	joined := joinCommandSpecs(commands)
	for _, want := range []string{
		"schtasks.exe",
		"/Create",
		"/TN",
		"SpaceDataNetworkDaemon",
		"/SC",
		"ONLOGON",
		`"C:\Program Files\SpaceDataNetwork\bin\spacedatanetwork.exe" daemon --config "C:\Users\tj\.spacedatanetwork\config.yaml"`,
		"/Run",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("windows scheduled task commands missing %q:\n%s", want, joined)
		}
	}
}

func TestServiceActionPlansUseNativeUserServiceManagers(t *testing.T) {
	spec := serviceSpec{
		Executable: "/opt/spacedatanetwork/bin/spacedatanetwork",
		ConfigPath: "/home/tj/.spacedatanetwork/config.yaml",
		WorkingDir: "/opt/spacedatanetwork",
	}

	startPlan, err := planServiceAction("linux", serviceActionStart, spec)
	if err != nil {
		t.Fatalf("plan start: %v", err)
	}
	if !strings.Contains(joinCommandSpecs(startPlan.Commands), "systemctl --user enable --now spacedatanetwork.service") {
		t.Fatalf("linux start plan did not enable and start service:\n%s", joinCommandSpecs(startPlan.Commands))
	}

	stopPlan, err := planServiceAction("linux", serviceActionStop, spec)
	if err != nil {
		t.Fatalf("plan stop: %v", err)
	}
	if !strings.Contains(joinCommandSpecs(stopPlan.Commands), "systemctl --user disable --now spacedatanetwork.service") {
		t.Fatalf("linux stop plan did not disable and stop service:\n%s", joinCommandSpecs(stopPlan.Commands))
	}
}

func TestPlanRemoveCurrentInstallPreservesDataByDefault(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	runtimeDir := filepath.Join(root, "runtime")
	mustMkdir(t, binDir)
	mustMkdir(t, runtimeDir)
	mustWriteFile(t, filepath.Join(root, "manifest.json"), "{}")
	exe := filepath.Join(binDir, executableNameForTest("spacedatanetwork"))
	mustWriteFile(t, exe, "")

	aliasDir := t.TempDir()
	alias := filepath.Join(aliasDir, executableNameForTest("sdn"))
	mustSymlinkOrSkip(t, exe, alias)

	plan, err := planRemoveCurrentInstall(removeOptions{
		Executable:  exe,
		PathEntries: []string{aliasDir},
		HomeDir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("plan remove: %v", err)
	}

	wantRoot := mustEvalSymlinks(t, root)
	if plan.BundleRoot != wantRoot {
		t.Fatalf("bundle root = %q, want %q", plan.BundleRoot, wantRoot)
	}
	if !containsPath(plan.Aliases, alias) {
		t.Fatalf("aliases = %#v, want %q", plan.Aliases, alias)
	}
	if len(plan.DataPaths) != 0 {
		t.Fatalf("data paths = %#v, want none without purge-data", plan.DataPaths)
	}
}

func TestPlanRemoveCurrentInstallPurgesDataWhenRequested(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	mustMkdir(t, binDir)
	mustMkdir(t, filepath.Join(root, "runtime"))
	mustWriteFile(t, filepath.Join(root, "manifest.json"), "{}")
	exe := filepath.Join(binDir, executableNameForTest("spacedatanetwork"))
	mustWriteFile(t, exe, "")

	plan, err := planRemoveCurrentInstall(removeOptions{
		Executable: exe,
		HomeDir:    home,
		PurgeData:  true,
	})
	if err != nil {
		t.Fatalf("plan remove: %v", err)
	}

	wantData := filepath.Join(home, ".spacedatanetwork")
	if !containsPath(plan.DataPaths, wantData) {
		t.Fatalf("data paths = %#v, want %q", plan.DataPaths, wantData)
	}
}

func joinCommandSpecs(commands []commandSpec) string {
	var parts []string
	for _, command := range commands {
		parts = append(parts, strings.Join(append([]string{command.Name}, command.Args...), " "))
	}
	return strings.Join(parts, "\n")
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func mustWriteFile(t *testing.T, path string, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustSymlinkOrSkip(t *testing.T, oldname string, newname string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixture requires elevated permissions on Windows")
	}
	if err := os.Symlink(oldname, newname); err != nil {
		t.Fatalf("symlink %s -> %s: %v", newname, oldname, err)
	}
}

func executableNameForTest(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func containsPath(paths []string, want string) bool {
	for _, path := range paths {
		if path == want {
			return true
		}
	}
	return false
}

func mustEvalSymlinks(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("eval symlinks %s: %v", path, err)
	}
	return resolved
}

package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/spacedatanetwork/sdn-server/internal/config"
)

const (
	serviceLabel          = "org.spacedatanetwork.daemon"
	serviceUnitName       = "spacedatanetwork.service"
	windowsTaskName       = "SpaceDataNetworkDaemon"
	serviceDisplayName    = "Space Data Network daemon"
	removeScriptSleepSecs = 2
)

type serviceAction string

const (
	serviceActionStart     serviceAction = "start"
	serviceActionStop      serviceAction = "stop"
	serviceActionRestart   serviceAction = "restart"
	serviceActionInstall   serviceAction = "install"
	serviceActionUninstall serviceAction = "uninstall"
	serviceActionStatus    serviceAction = "status"
)

type serviceSpec struct {
	Executable string
	ConfigPath string
	WorkingDir string
	HomeDir    string
	UserID     string
}

type commandSpec struct {
	Name         string
	Args         []string
	AllowFailure bool
}

type fileSpec struct {
	Path string
	Mode os.FileMode
	Body string
}

type servicePlan struct {
	Files        []fileSpec
	Commands     []commandSpec
	RemoveFiles  []string
	PostCommands []commandSpec
}

type removeOptions struct {
	Executable  string
	PathEntries []string
	HomeDir     string
	PurgeData   bool
	DryRun      bool
}

type removePlan struct {
	Executable string
	BundleRoot string
	Aliases    []string
	DataPaths  []string
	Delayed    bool
}

var (
	removeDryRun    bool
	removePurgeData bool

	serviceCommandRunner = runNativeCommand
)

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start SDN as a persistent background service",
	Long:  "Install, enable, and start the user-scoped Space Data Network background service.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runServiceAction(cmd, serviceActionStart)
	},
}

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the persistent SDN background service",
	Long:  "Stop and disable the user-scoped Space Data Network background service.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runServiceAction(cmd, serviceActionStop)
	},
}

var restartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Restart the persistent SDN background service",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runServiceAction(cmd, serviceActionRestart)
	},
}

var serviceCmd = &cobra.Command{
	Use:   "service",
	Short: "Manage the persistent SDN background service",
}

var serviceStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show native service status",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runServiceAction(cmd, serviceActionStatus)
	},
}

var serviceInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install and start the persistent SDN background service",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runServiceAction(cmd, serviceActionInstall)
	},
}

var serviceUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Stop and uninstall the persistent SDN background service",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runServiceAction(cmd, serviceActionUninstall)
	},
}

var removeCmd = &cobra.Command{
	Use:   "remove",
	Short: "Remove the current SDN CLI install",
	Long:  "Stop the background service, remove aliases that point at this install, and remove the current self-contained bundle. Node identity and data are preserved unless --purge-data is passed.",
	RunE:  runRemove,
}

func init() {
	serviceCmd.AddCommand(serviceStatusCmd, serviceInstallCmd, serviceUninstallCmd)
	removeCmd.Flags().BoolVar(&removeDryRun, "dry-run", false, "print the remove plan without deleting files")
	removeCmd.Flags().BoolVar(&removePurgeData, "purge-data", false, "also remove the current user's SDN config, identity, and data")
	rootCmd.AddCommand(startCmd, stopCmd, restartCmd, removeCmd, serviceCmd)
}

func runServiceAction(cmd *cobra.Command, action serviceAction) error {
	if action == serviceActionStart || action == serviceActionInstall || action == serviceActionRestart {
		if err := runInit(cmd, nil); err != nil {
			return err
		}
	}

	spec, err := currentServiceSpec()
	if err != nil {
		return err
	}
	plan, err := planServiceAction(runtime.GOOS, action, spec)
	if err != nil {
		return err
	}
	if err := executeServicePlan(cmd.Context(), plan); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "service_%s=ok\n", action)
	return nil
}

func currentServiceSpec() (serviceSpec, error) {
	actualExecutable, err := os.Executable()
	if err != nil {
		return serviceSpec{}, fmt.Errorf("resolve current executable: %w", err)
	}
	actualExecutable, _ = filepath.Abs(actualExecutable)
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return serviceSpec{}, fmt.Errorf("resolve home directory: %w", err)
	}
	cfgPath := strings.TrimSpace(configPath)
	if cfgPath == "" {
		cfgPath = config.DefaultPath()
	}
	if abs, err := filepath.Abs(cfgPath); err == nil {
		cfgPath = abs
	}

	executable := serviceExecutablePath(actualExecutable, resolveInvokedExecutable())
	workingDir := filepath.Dir(executable)
	if root := detectBundleRoot(actualExecutable); root != "" {
		workingDir = root
	}
	return serviceSpec{
		Executable: executable,
		ConfigPath: cfgPath,
		WorkingDir: workingDir,
		HomeDir:    homeDir,
		UserID:     currentUserID(),
	}, nil
}

func serviceExecutablePath(actualExecutable string, invokedExecutable string) string {
	if root := detectBundleRoot(actualExecutable); root != "" {
		candidate := filepath.Join(root, "bin", executableName("spacedatanetwork"))
		if fileExists(candidate) {
			return candidate
		}
	}
	if strings.TrimSpace(invokedExecutable) != "" && fileExists(invokedExecutable) {
		return invokedExecutable
	}
	return actualExecutable
}

func resolveInvokedExecutable() string {
	if len(os.Args) == 0 || strings.TrimSpace(os.Args[0]) == "" {
		return ""
	}
	invoked := os.Args[0]
	if filepath.IsAbs(invoked) {
		if abs, err := filepath.Abs(invoked); err == nil {
			return abs
		}
		return invoked
	}
	found, err := exec.LookPath(invoked)
	if err != nil {
		return ""
	}
	if abs, err := filepath.Abs(found); err == nil {
		return abs
	}
	return found
}

func planServiceAction(osName string, action serviceAction, spec serviceSpec) (servicePlan, error) {
	switch action {
	case serviceActionRestart:
		stopPlan, err := planServiceAction(osName, serviceActionStop, spec)
		if err != nil {
			return servicePlan{}, err
		}
		startPlan, err := planServiceAction(osName, serviceActionStart, spec)
		if err != nil {
			return servicePlan{}, err
		}
		return mergeServicePlans(stopPlan, startPlan), nil
	case serviceActionStart:
		return planServiceAction(osName, serviceActionInstall, spec)
	}

	switch osName {
	case "darwin":
		return planLaunchdServiceAction(action, spec)
	case "linux":
		return planSystemdUserServiceAction(action, spec)
	case "windows":
		return planWindowsScheduledTaskAction(action, spec)
	default:
		return servicePlan{}, fmt.Errorf("persistent service management is not supported on %s", osName)
	}
}

func mergeServicePlans(plans ...servicePlan) servicePlan {
	var merged servicePlan
	for _, plan := range plans {
		merged.Files = append(merged.Files, plan.Files...)
		merged.Commands = append(merged.Commands, plan.Commands...)
		merged.RemoveFiles = append(merged.RemoveFiles, plan.RemoveFiles...)
		merged.PostCommands = append(merged.PostCommands, plan.PostCommands...)
	}
	return merged
}

func planLaunchdServiceAction(action serviceAction, spec serviceSpec) (servicePlan, error) {
	plistPath := launchAgentPath(spec)
	domain := "gui/" + spec.UserID
	if strings.TrimSpace(spec.UserID) == "" {
		domain = "gui/" + currentUserID()
	}
	serviceTarget := domain + "/" + serviceLabel

	switch action {
	case serviceActionInstall:
		return servicePlan{
			Files: []fileSpec{{Path: plistPath, Mode: 0o644, Body: renderLaunchAgentPlist(spec)}},
			Commands: []commandSpec{
				{Name: "launchctl", Args: []string{"bootout", serviceTarget}, AllowFailure: true},
				{Name: "launchctl", Args: []string{"bootstrap", domain, plistPath}},
				{Name: "launchctl", Args: []string{"enable", serviceTarget}},
				{Name: "launchctl", Args: []string{"kickstart", "-k", serviceTarget}},
			},
		}, nil
	case serviceActionStop:
		return servicePlan{Commands: []commandSpec{
			{Name: "launchctl", Args: []string{"disable", serviceTarget}, AllowFailure: true},
			{Name: "launchctl", Args: []string{"bootout", serviceTarget}, AllowFailure: true},
		}}, nil
	case serviceActionUninstall:
		return servicePlan{
			Commands: []commandSpec{
				{Name: "launchctl", Args: []string{"disable", serviceTarget}, AllowFailure: true},
				{Name: "launchctl", Args: []string{"bootout", serviceTarget}, AllowFailure: true},
			},
			RemoveFiles: []string{plistPath},
		}, nil
	case serviceActionStatus:
		return servicePlan{Commands: []commandSpec{{Name: "launchctl", Args: []string{"print", serviceTarget}}}}, nil
	default:
		return servicePlan{}, fmt.Errorf("unsupported launchd service action %q", action)
	}
}

func planSystemdUserServiceAction(action serviceAction, spec serviceSpec) (servicePlan, error) {
	unitPath := systemdUserUnitPath(spec)
	switch action {
	case serviceActionInstall:
		return servicePlan{
			Files: []fileSpec{{Path: unitPath, Mode: 0o644, Body: renderSystemdUserUnit(spec)}},
			Commands: []commandSpec{
				{Name: "systemctl", Args: []string{"--user", "daemon-reload"}},
				{Name: "systemctl", Args: []string{"--user", "enable", "--now", serviceUnitName}},
			},
		}, nil
	case serviceActionStop:
		return servicePlan{Commands: []commandSpec{
			{Name: "systemctl", Args: []string{"--user", "disable", "--now", serviceUnitName}, AllowFailure: true},
		}}, nil
	case serviceActionUninstall:
		return servicePlan{
			Commands: []commandSpec{
				{Name: "systemctl", Args: []string{"--user", "disable", "--now", serviceUnitName}, AllowFailure: true},
			},
			RemoveFiles:  []string{unitPath},
			PostCommands: []commandSpec{{Name: "systemctl", Args: []string{"--user", "daemon-reload"}}},
		}, nil
	case serviceActionStatus:
		return servicePlan{Commands: []commandSpec{{Name: "systemctl", Args: []string{"--user", "status", "--no-pager", serviceUnitName}}}}, nil
	default:
		return servicePlan{}, fmt.Errorf("unsupported systemd service action %q", action)
	}
}

func planWindowsScheduledTaskAction(action serviceAction, spec serviceSpec) (servicePlan, error) {
	switch action {
	case serviceActionInstall:
		return servicePlan{Commands: windowsServiceInstallCommands(spec)}, nil
	case serviceActionStop:
		return servicePlan{Commands: []commandSpec{
			{Name: "schtasks.exe", Args: []string{"/End", "/TN", windowsTaskName}, AllowFailure: true},
			{Name: "schtasks.exe", Args: []string{"/Change", "/TN", windowsTaskName, "/DISABLE"}, AllowFailure: true},
		}}, nil
	case serviceActionUninstall:
		return servicePlan{Commands: []commandSpec{
			{Name: "schtasks.exe", Args: []string{"/End", "/TN", windowsTaskName}, AllowFailure: true},
			{Name: "schtasks.exe", Args: []string{"/Delete", "/TN", windowsTaskName, "/F"}, AllowFailure: true},
		}}, nil
	case serviceActionStatus:
		return servicePlan{Commands: []commandSpec{{Name: "schtasks.exe", Args: []string{"/Query", "/TN", windowsTaskName, "/V", "/FO", "LIST"}}}}, nil
	default:
		return servicePlan{}, fmt.Errorf("unsupported Windows scheduled task action %q", action)
	}
}

func windowsServiceInstallCommands(spec serviceSpec) []commandSpec {
	return []commandSpec{
		{
			Name: "schtasks.exe",
			Args: []string{
				"/Create",
				"/F",
				"/TN", windowsTaskName,
				"/SC", "ONLOGON",
				"/RL", "LIMITED",
				"/TR", windowsTaskCommandLine(spec),
			},
		},
		{Name: "schtasks.exe", Args: []string{"/Change", "/TN", windowsTaskName, "/ENABLE"}},
		{Name: "schtasks.exe", Args: []string{"/Run", "/TN", windowsTaskName}},
	}
}

func executeServicePlan(ctx context.Context, plan servicePlan) error {
	for _, file := range plan.Files {
		if err := os.MkdirAll(filepath.Dir(file.Path), 0o755); err != nil {
			return fmt.Errorf("create service file directory %s: %w", filepath.Dir(file.Path), err)
		}
		if err := os.WriteFile(file.Path, []byte(file.Body), file.Mode); err != nil {
			return fmt.Errorf("write service file %s: %w", file.Path, err)
		}
	}
	for _, command := range plan.Commands {
		if err := serviceCommandRunner(ctx, command); err != nil {
			if command.AllowFailure {
				continue
			}
			return err
		}
	}
	for _, path := range plan.RemoveFiles {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove service file %s: %w", path, err)
		}
	}
	for _, command := range plan.PostCommands {
		if err := serviceCommandRunner(ctx, command); err != nil {
			if command.AllowFailure {
				continue
			}
			return err
		}
	}
	return nil
}

func runNativeCommand(ctx context.Context, command commandSpec) error {
	cmd := exec.CommandContext(ctx, command.Name, command.Args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s failed: %w\n%s", command.Name, strings.Join(command.Args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func renderLaunchAgentPlist(spec serviceSpec) string {
	args := append([]string{spec.Executable}, daemonServiceArgs(spec)...)
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n")
	b.WriteString("<dict>\n")
	writePlistString(&b, "Label", serviceLabel)
	b.WriteString("\t<key>ProgramArguments</key>\n")
	b.WriteString("\t<array>\n")
	for _, arg := range args {
		b.WriteString("\t\t<string>")
		b.WriteString(xmlEscape(arg))
		b.WriteString("</string>\n")
	}
	b.WriteString("\t</array>\n")
	writePlistString(&b, "WorkingDirectory", spec.WorkingDir)
	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")
	b.WriteString("\t<key>KeepAlive</key>\n\t<true/>\n")
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

func writePlistString(b *strings.Builder, key string, value string) {
	b.WriteString("\t<key>")
	b.WriteString(xmlEscape(key))
	b.WriteString("</key>\n\t<string>")
	b.WriteString(xmlEscape(value))
	b.WriteString("</string>\n")
}

func renderSystemdUserUnit(spec serviceSpec) string {
	execParts := append([]string{systemdQuote(spec.Executable)}, quoteArgsForSystemd(daemonServiceArgs(spec))...)
	return strings.Join([]string{
		"[Unit]",
		"Description=" + serviceDisplayName,
		"After=network-online.target",
		"Wants=network-online.target",
		"",
		"[Service]",
		"Type=simple",
		"WorkingDirectory=" + systemdQuote(spec.WorkingDir),
		"ExecStart=" + strings.Join(execParts, " "),
		"Restart=always",
		"RestartSec=10",
		"",
		"[Install]",
		"WantedBy=default.target",
		"",
	}, "\n")
}

func daemonServiceArgs(spec serviceSpec) []string {
	args := []string{"daemon"}
	if strings.TrimSpace(spec.ConfigPath) != "" {
		args = append(args, "--config", spec.ConfigPath)
	}
	return args
}

func quoteArgsForSystemd(args []string) []string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, systemdQuote(arg))
	}
	return quoted
}

func systemdQuote(value string) string {
	if value == "" {
		return `""`
	}
	if strings.ContainsAny(value, " \t\n\"'\\") {
		return strconv.Quote(value)
	}
	return value
}

func windowsTaskCommandLine(spec serviceSpec) string {
	args := append([]string{windowsCommandQuote(spec.Executable)}, daemonServiceArgs(spec)...)
	for index, arg := range args {
		if index == 0 {
			continue
		}
		args[index] = windowsCommandQuote(arg)
	}
	return strings.Join(args, " ")
}

func windowsCommandQuote(value string) string {
	if value == "" {
		return `""`
	}
	if !strings.ContainsAny(value, " \t\"\\") {
		return value
	}
	return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
}

func launchAgentPath(spec serviceSpec) string {
	home := spec.HomeDir
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, "Library", "LaunchAgents", serviceLabel+".plist")
}

func systemdUserUnitPath(spec serviceSpec) string {
	home := spec.HomeDir
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, ".config", "systemd", "user", serviceUnitName)
}

func xmlEscape(value string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(value))
	return b.String()
}

func runRemove(cmd *cobra.Command, args []string) error {
	spec, err := currentServiceSpec()
	if err != nil {
		return err
	}
	uninstallPlan, err := planServiceAction(runtime.GOOS, serviceActionUninstall, spec)
	if err != nil {
		return err
	}
	plan, err := planRemoveCurrentInstall(removeOptions{
		Executable:  currentActualExecutable(),
		PathEntries: filepath.SplitList(os.Getenv("PATH")),
		HomeDir:     spec.HomeDir,
		PurgeData:   removePurgeData,
		DryRun:      removeDryRun,
	})
	if err != nil {
		return err
	}

	if removeDryRun {
		printRemovePlan(cmd, plan)
		return nil
	}
	_ = executeServicePlan(cmd.Context(), allowServicePlanFailures(uninstallPlan))
	if err := executeRemovePlan(plan); err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), "remove=ok")
	return nil
}

func currentActualExecutable() string {
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	if abs, err := filepath.Abs(executable); err == nil {
		return abs
	}
	return executable
}

func planRemoveCurrentInstall(options removeOptions) (removePlan, error) {
	executable := options.Executable
	if executable == "" {
		executable = currentActualExecutable()
	}
	if executable == "" {
		return removePlan{}, fmt.Errorf("could not resolve current executable")
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	if abs, err := filepath.Abs(executable); err == nil {
		executable = abs
	}
	root := detectBundleRoot(executable)
	if root == "" {
		return removePlan{}, fmt.Errorf("current executable is not inside a self-contained SDN install; refusing to remove %s", executable)
	}

	plan := removePlan{
		Executable: executable,
		BundleRoot: root,
		Aliases:    detectInstallAliases(root, executable, options.PathEntries),
		Delayed:    runtime.GOOS == "windows",
	}
	if options.PurgeData {
		home := options.HomeDir
		if home == "" {
			home, _ = os.UserHomeDir()
		}
		if home != "" {
			plan.DataPaths = append(plan.DataPaths, filepath.Join(home, ".spacedatanetwork"))
		}
	}
	return plan, nil
}

func executeRemovePlan(plan removePlan) error {
	if runtime.GOOS == "windows" {
		return startWindowsDelayedRemove(plan)
	}
	for _, alias := range plan.Aliases {
		if err := os.Remove(alias); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove alias %s: %w", alias, err)
		}
	}
	for _, path := range plan.DataPaths {
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("remove data path %s: %w", path, err)
		}
	}
	if err := os.RemoveAll(plan.BundleRoot); err != nil {
		return fmt.Errorf("remove bundle %s: %w", plan.BundleRoot, err)
	}
	return nil
}

func startWindowsDelayedRemove(plan removePlan) error {
	scriptPath := filepath.Join(os.TempDir(), fmt.Sprintf("spacedatanetwork-remove-%d.ps1", time.Now().UnixNano()))
	body := renderWindowsRemoveScript(os.Getpid(), plan)
	if err := os.WriteFile(scriptPath, []byte(body), 0o600); err != nil {
		return fmt.Errorf("write delayed remove script: %w", err)
	}
	cmd := exec.Command("powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", scriptPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start delayed remove script: %w", err)
	}
	return nil
}

func renderWindowsRemoveScript(pid int, plan removePlan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Start-Sleep -Seconds %d\n", removeScriptSleepSecs)
	fmt.Fprintf(&b, "Wait-Process -Id %d -ErrorAction SilentlyContinue\n", pid)
	for _, alias := range plan.Aliases {
		fmt.Fprintf(&b, "Remove-Item -LiteralPath %s -Force -ErrorAction SilentlyContinue\n", powershellQuote(alias))
	}
	for _, dataPath := range plan.DataPaths {
		fmt.Fprintf(&b, "Remove-Item -LiteralPath %s -Recurse -Force -ErrorAction SilentlyContinue\n", powershellQuote(dataPath))
	}
	fmt.Fprintf(&b, "Remove-Item -LiteralPath %s -Recurse -Force -ErrorAction SilentlyContinue\n", powershellQuote(plan.BundleRoot))
	b.WriteString("Remove-Item -LiteralPath $PSCommandPath -Force -ErrorAction SilentlyContinue\n")
	return b.String()
}

func powershellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func printRemovePlan(cmd *cobra.Command, plan removePlan) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "bundle=%s\n", plan.BundleRoot)
	for _, alias := range plan.Aliases {
		fmt.Fprintf(out, "alias=%s\n", alias)
	}
	for _, dataPath := range plan.DataPaths {
		fmt.Fprintf(out, "data=%s\n", dataPath)
	}
}

func allowServicePlanFailures(plan servicePlan) servicePlan {
	for index := range plan.Commands {
		plan.Commands[index].AllowFailure = true
	}
	for index := range plan.PostCommands {
		plan.PostCommands[index].AllowFailure = true
	}
	return plan
}

func detectInstallAliases(bundleRoot string, executable string, pathEntries []string) []string {
	seen := map[string]bool{}
	var aliases []string
	for _, dir := range pathEntries {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		for _, name := range []string{executableName("spacedatanetwork"), executableName("sdn")} {
			candidate := filepath.Join(dir, name)
			if seen[candidate] || !fileExists(candidate) {
				continue
			}
			if installAliasPointsAt(candidate, bundleRoot, executable) {
				seen[candidate] = true
				aliases = append(aliases, candidate)
			}
		}
	}
	return aliases
}

func installAliasPointsAt(candidate string, bundleRoot string, executable string) bool {
	resolved := candidate
	if target, err := filepath.EvalSymlinks(candidate); err == nil {
		resolved = target
	}
	if abs, err := filepath.Abs(resolved); err == nil {
		resolved = abs
	}
	if samePath(resolved, executable) {
		return true
	}
	return pathWithin(resolved, bundleRoot)
}

func detectBundleRoot(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	current := path
	if resolved, err := filepath.EvalSymlinks(current); err == nil {
		current = resolved
	}
	if abs, err := filepath.Abs(current); err == nil {
		current = abs
	}
	info, err := os.Stat(current)
	if err == nil && !info.IsDir() {
		current = filepath.Dir(current)
	}
	for {
		if isBundleRoot(current) {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
}

func isBundleRoot(path string) bool {
	if !fileExists(filepath.Join(path, "manifest.json")) {
		return false
	}
	if !dirExists(filepath.Join(path, "bin")) {
		return false
	}
	if !dirExists(filepath.Join(path, "runtime")) {
		return false
	}
	return true
}

func pathWithin(path string, root string) bool {
	if path == "" || root == "" {
		return false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func samePath(left string, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func executableName(name string) string {
	if runtime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(name), ".exe") {
		return name + ".exe"
	}
	return name
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func currentUserID() string {
	current, err := user.Current()
	if err != nil {
		return ""
	}
	return current.Uid
}

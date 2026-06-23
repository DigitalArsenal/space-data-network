# CLI Service Management Design

## Goal

Add first-version service management to the self-contained SDN CLI so users can run the node in the background across macOS, Linux, and Windows without manually keeping `spacedatanetwork daemon` open.

## Command Surface

The foreground daemon remains unchanged:

```bash
spacedatanetwork daemon
```

The new user-facing commands are:

```bash
spacedatanetwork start
spacedatanetwork stop
spacedatanetwork restart
spacedatanetwork remove
spacedatanetwork service status
spacedatanetwork service install
spacedatanetwork service uninstall
```

`start` installs the user-scoped background service if it is missing, enables it for future login/restart, and starts it now. `stop` stops the background service and disables future automatic starts. `restart` performs stop then start. `service install` and `service uninstall` expose the explicit install/remove service primitives.

`remove` stops and uninstalls the background service, removes the CLI aliases that point at the current install, and removes the current self-contained bundle. It preserves the user's SDN config, mnemonic, and data by default. `remove --purge-data` also removes the current user's SDN home directory after removing the install.

## Platform Model

The default service scope is user-scoped so the daemon uses the same config, mnemonic, storage directory, and wallet password resolution as interactive CLI commands.

- macOS uses a LaunchAgent at `~/Library/LaunchAgents/org.spacedatanetwork.daemon.plist`.
- Linux uses a `systemd --user` unit at `~/.config/systemd/user/spacedatanetwork.service`.
- Windows uses a per-user Scheduled Task named `SpaceDataNetworkDaemon`.

The service command runs the absolute current executable path with:

```bash
spacedatanetwork daemon --config <effective-config-path>
```

The unit/task working directory is the current bundle root when the executable is inside a self-contained bundle, otherwise the executable directory.

## Removal Model

`remove` detects the active executable with `os.Executable`, resolves aliases such as `spacedatanetwork` and `sdn` from `PATH`, and removes only aliases that point to the active bundle or executable. It then removes the active self-contained bundle root. Source-tree builds are not deleted unless their executable is inside a recognized self-contained bundle layout with `manifest.json`, `bin/`, and `runtime/`.

On Windows, the running executable cannot delete itself. The command writes a temporary PowerShell cleanup script, launches it detached, and exits. The script waits for the current process to exit before deleting aliases and the bundle directory.

## Errors

Service manager commands return actionable errors with the attempted native command. `remove` prints a dry-run plan when `--dry-run` is passed and refuses to delete data unless `--purge-data` is set.

## Testing

Focused Go tests cover:

- command registration and root help listing;
- macOS plist rendering;
- Linux systemd unit rendering;
- Windows Scheduled Task command construction;
- remove planning for bundle roots, aliases, and data purge behavior.

The release archive tests continue to prove self-contained bundles include the runtime dependencies that the service starts.

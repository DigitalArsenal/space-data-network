# Install Bootstrap Identity Export Design

## Problem

The first public SDN CLI release installs successfully, but `spacedatanetwork show-identity` fails immediately after install because `spacedatanetwork init` only writes config and does not create the encrypted node mnemonic. Identity creation currently happens lazily during daemon startup. That is the wrong first-run contract: the installed CLI must be ready to identify itself and join the network without requiring users to know daemon side effects.

The CLI also needs a scriptable way to print the node contact record/EPM in user-facing formats: text, JSON, CSV, and QR code.

## Design

`spacedatanetwork init` becomes the canonical first-run bootstrap command. It writes config, resolves the bundled HD wallet WASM from the release bundle, creates the encrypted mnemonic and derived identity if missing, and preserves any existing mnemonic. The command is idempotent and does not rotate identity on repeated runs.

The public installer runs `spacedatanetwork init` after installing command links on Unix/macOS. `SDN_SKIP_INIT=1` disables this only for packaging or CI cases that must avoid touching user state.

Identity-related commands resolve the HD wallet WASM from the release bundle first-class. Source-tree fallbacks are development conveniences, not the release contract.

The CLI adds `spacedatanetwork identity export --format text|json|csv|qrcode`. The command reads from the local daemon EPM endpoints so it uses the same contact record that every other query surface uses. Text emits the vCard, JSON emits EPM JSON, CSV emits one header row and one data row for common EPM fields, and QR emits terminal QR output derived from the node vCard. It does not print mnemonic material.

## Testing

Tests cover installer bootstrap behavior, init mnemonic creation and idempotence, bundle WASM resolution for identity commands, and export formatting. Release smoke tests continue to install from the public script and verify the installed CLI can initialize.

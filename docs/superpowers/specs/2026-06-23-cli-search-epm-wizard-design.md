# CLI Search And EPM Wizard Design

## Goal

Add scriptable CLI search for SDN providers, Space Data Standards, and local data-source metadata, plus an interactive CLI wizard for creating or updating the node EPM/vCard contact record.

## CLI Surface

Search is a top-level command group:

```sh
spacedatanetwork search providers [query] --schema OMM --format table
spacedatanetwork search standards [query] --format json
spacedatanetwork search data [query] --schema CAT --provider-id space-data-network-02 --format csv
```

`--format` accepts `table`, `json`, and `csv`. `table` is the default row form for human terminals. JSON emits a stable object with `count` and `results`. CSV emits a header row and data rows with the same field names as JSON.

The EPM wizard lives under the existing identity command group:

```sh
spacedatanetwork identity wizard
spacedatanetwork identity wizard --format json
spacedatanetwork identity wizard --format csv
spacedatanetwork identity wizard --format flatbuffer --output epm.fbs
spacedatanetwork identity wizard --format qrcode
spacedatanetwork identity export --format flatbuffer --output epm.fbs
```

`identity export` keeps the existing non-interactive export role and gains `flatbuffer`. `identity wizard` edits or creates the local public EPM data first, then emits the requested output format.

## Search Behavior

Provider search reads the local FlatSQL-backed directory index, using the same indexed EPM records used by `/api/directory/nodes`. Results include provider identity fields from EPM directory records: peer ID, display name/DN, legal name, EPM CID, source, updated time, and known public aliases such as chain addresses or name-service values when present in EPM JSON.

When `--schema` is provided, provider search enriches provider rows with local replica/source stats for that SDS schema: provider ID, source name, batch ID, query profile, local rows, pinned rows, cached bytes, pinned bytes, head, high-water mark, and last synced time. Provider identifier matching reuses the existing sync CLI resolver, including direct provider IDs, peer IDs, public keys, IPFS/IPNS, xpubs, Bitcoin/Ethereum/Solana addresses, ENS, and SNS.

Standards search reads the canonical SDS validator registry. It matches by schema code, schema filename, and available description text. It also joins local FlatSQL schema summaries so a standard can show local record count and total bytes. This command does not require a running daemon.

Data search reads local FlatSQL source summaries and local replica stats. It searches metadata, not full record payload bytes. Filters include `--schema`, `--provider-id`, `--source-name`, `--batch-id`, and `--query-profile`. Results identify which local datasets are available for query or sync.

## EPM Wizard Behavior

The wizard prompts for public identity/contact fields and writes an EPM record that can also be represented as vCard. Required prompts are:

- display name / DN
- entity type: node, provider, organization, or user
- legal name
- email
- telephone
- website or URL
- provider ID
- public multiaddrs
- Bitcoin, Ethereum, and Solana addresses
- ENS and SNS names

Every prompt supports accepting an existing value when a local EPM already exists. Empty optional fields are omitted. The wizard must never prompt for or print mnemonic, xpriv, private signing key, or private encryption key material.

After confirmation, the wizard updates the local EPM source of truth used by the daemon and directory index. If the daemon is not running, it updates local storage directly through the same EPM service/storage code used by `init` and identity export.

## Output Formats

Search outputs:

- `table`: aligned rows, one result per line plus a header.
- `json`: stable API-shaped payload with `count` and `results`.
- `csv`: stable header names matching JSON fields.

EPM wizard and identity export outputs:

- `json`: canonical public EPM JSON.
- `csv`: one header row and one data row for the public EPM fields.
- `flatbuffer`: canonical EPM FlatBuffer bytes written to `--output`; if no output path is provided, bytes go to stdout.
- `qrcode`: terminal QR code for the vCard representation.
- `text` or `vcard`: vCard text for compatibility with the current default identity export behavior.

FlatBuffer output must be raw bytes, not base64, unless a future `--format flatbuffer-json` is added. JSON remains the script-friendly text encoding.

## Data Flow

Search opens the configured local FlatSQL store directly, matching `sync status`, so it works without browser authentication. It uses:

- `storage.QueryDirectory` for provider EPM rows.
- `storage.DataSummary` for schema/source counts.
- `storage.LocalReplicaStats` for sync/pin/materialization state.
- `sds.Validator` for standards normalization and schema existence checks.

The wizard uses the identity/EPM service instead of hand-writing a sidecar JSON file. Updating the EPM must refresh the directory record so provider search can find the local node/provider identity immediately.

## Error Handling

Unknown schema names return a clear error such as `unknown schema "XYZ"` and accept both `OMM` and `OMM.fbs` forms.

Unsupported formats return a clear error listing valid formats. CSV and JSON must be deterministic for tests. Table output should print a header even when there are zero results, followed by no data rows and an exit status of zero.

Wizard cancellation before confirmation exits without modifying the existing EPM. Invalid public addresses are rejected when lightweight validation exists; otherwise the field is accepted as opaque public contact metadata.

## Testing

Tests cover:

- root help lists `search` and `identity wizard`;
- provider search returns table, JSON, and CSV from seeded directory and replica stats;
- standards search normalizes schema names and joins local counts;
- data search filters by schema/provider/source and formats table, JSON, and CSV;
- wizard prompt flow preserves existing values, omits private key material, and writes the same EPM that export reads;
- `identity export --format flatbuffer` emits raw EPM FlatBuffer bytes and `--format qrcode` still emits vCard QR text.

Release smoke should install the public CLI, run `identity wizard` in non-interactive `--set key=value` mode or with piped answers, export JSON/CSV/FlatBuffer/QR, and run at least one local `search standards OMM --format json` command.

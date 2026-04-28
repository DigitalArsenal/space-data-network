# Space Data Network: AI-Agentic Coding First

## Executive Positioning

Space Data Network should be pitched as the standards-native data and execution
layer for space-aware AI systems, and as a space-data infrastructure stack
designed for AI coding agents as a primary developer interface.

The claim is not that SDN has an AI chatbot bolted onto it. The stronger claim
is that SDN is contract-first infrastructure: schemas, WebAssembly modules,
node deployments, marketplace listings, signatures, encryption records, and
OrbPro integrations can all be described as executable tasks with explicit
inputs, files, commands, artifacts, and verification gates.

That makes SDN unusually well suited for LLM-based engineering workflows:

- an agent can create a standards-compliant module instead of guessing an API
- an agent can deploy a node with Docker or bare metal steps instead of reading
  scattered operator docs
- an agent can validate every claim with repo-local commands
- an agent can route changes to the correct repository instead of mixing schema,
  runtime, network, and application responsibilities
- an agent can fail closed when validation, signing, encryption, or deployment
  prerequisites are missing

The phrase to lead with:

> Space Data Network is an open, verifiable, AI-agent-operable network for
> standardized space data, portable WebAssembly analytics, and deployable
> peer-to-peer infrastructure.

The architecture phrase to repeat:

> Agents generate artifacts; SDN verifies, signs, routes, stores, and delivers
> them.

The provider-facing phrase to lead with:

> Models should not guess orbital state from stale training data. SDN gives
> space-aware AI systems live typed data, signed provenance, and deterministic
> WebAssembly tools beside probabilistic models.

## What "AI-First" Means Here

AI-first should mean operational clarity, not marketing language.

For SDN, AI-first means:

1. **Agent-readable ownership boundaries.** Every task starts by identifying the
   owning repository and the surfaces the agent must not edit.
2. **Machine-verifiable contracts.** Schemas, manifests, module ABI, bundle
   layout, publication records, and node deployment shapes are documented as
   contracts with validation commands.
3. **Golden-path task packets.** Common outcomes are written as copyable
   markdown packets: intent, prerequisites, files, commands, expected artifacts,
   verification, and failure handling.
4. **Runnable examples before abstractions.** The first screen for an agent is
   not a concept page. It is a working module, node, schema, or OrbPro demo path.
5. **Fail-closed safety.** Agents are told when not to proceed: missing schema
   release, failed compliance check, unsigned publication record, production
   config containing dev keys, missing WasmEdge, broken browser portability, or
   unverifiable artifact hash.
6. **Provider-neutral execution.** The docs should work with Claude, ChatGPT,
   Gemini, local coding agents, IDE copilots, and future inference providers
   because the task structure is plain markdown plus shell commands.
7. **Live data plus deterministic tools.** LLMs reason over current, typed,
   signed SDS records and call portable WASM tools for propagation, conversion,
   validation, filtering, and analysis rather than approximating those functions
   from model weights.
8. **Operator-supervised automation.** Agents can draft configs, explain
   port/TLS/DHT choices, run preflight checks, and summarize logs. Operators
   approve trust, identity, firewall exposure, production keys, paid access, and
   service changes.

Avoid saying:

- "AI writes space software automatically."
- "Anyone can deploy mission-critical infrastructure safely with one prompt."
- "The model understands orbital mechanics."
- "The AI replaces verification."

Say instead:

- "AI agents can contribute safely because the system exposes precise contracts
  and verification gates."
- "The agent produces artifacts; the repo proves whether those artifacts comply."
- "Human operators remain responsible for production approval, keys, and trust
  policy."
- "Content addressing detects tampering; signatures bind authorship; EPM and
  local policy decide trust."
- "SDN is not asking operators to trust generated code; it gives them repeatable
  ways to reject bad artifacts."

## Four-Repository Architecture

The AI-first story only works if agents know where work belongs.

| Repository | Agent Role | Owns | Does Not Own |
| --- | --- | --- | --- |
| `spacedatastandards.org` | Define the data language | FlatBuffer schemas, generated bindings, schema docs, canonical records such as `PLG`, `PIV`, `REC`, `PNM`, `EPM` | SDN node behavior, OrbPro UI, SDK-local shadow schemas |
| `space-data-module-sdk` | Build compliant modules | manifest/codecs, module ABI, compliance validation, compiler flow, `sds.bundle`, signing/encryption helpers, browser/WasmEdge harnesses | SDN control-plane UI, node operations, OrbPro application flows |
| `space-data-network` | Run the network | full nodes, edge relays, `sdn-js`, encrypted module delivery, Docker/bare-metal deployment, provider discovery, storefront/runtime APIs | canonical schema source, parallel module manifest specs, OrbPro scene behavior |
| `OrbPro` | Use modules in an operational app | Cesium/OrbPro runtime, Sandcastle demos, SDN module loading UX, operator workflows, visual validation | schema standards, node deployment scripts, generic SDK compliance |

This should become the core routing rule in every agent-facing guide:

```text
Intent -> Owning repo -> Contract docs -> Files -> Commands -> Validation -> Artifact
```

The broader principle:

```text
Contracts before prompts:
FlatBuffers -> Wasm manifests -> CIDs -> EPM identities -> PLG listings -> local verification gates
```

## The Trust Boundary Is Not The Model

This should be a central message for technical audiences.

Language models propose, scaffold, summarize, and operate tools. They do not
define truth, trust, compliance, or operational authority.

Acceptance comes from:

- canonical SDS schemas and file identifiers
- FlatBuffer binary validation
- module manifests and capability declarations
- SDK compliance checks
- reproducible build outputs
- content addressing through hashes and CIDs
- signatures over publication and identity records
- EPM-backed trust policy
- human review and operator approval

The practical trust boundary is:

```text
model output -> repo validation -> signed artifact -> operator policy -> network delivery
```

Signatures prove authorship and integrity. They do not prove that data is true,
that an analysis is correct, or that a provider should be trusted. Those remain
policy and review decisions.

WebAssembly alone does not make modules safe. Safety comes from manifests,
capability boundaries, compliance validation, sandboxing, signing, encrypted
delivery, and review.

## Prompt To Proof

AI-first documentation should show the artifact chain from user intent to
verified network behavior:

```text
prompt
  -> manifest + source
  -> dist/isomorphic/module.wasm
  -> manifest/compliance/artifact checks
  -> sds.bundle or raw compliant wasm
  -> REC / PNM / ENC / PLG publication metadata
  -> encrypted module delivery
  -> SDK browser harness or WasmEdge invocation
  -> observable result in SDN or OrbPro
```

This chain is the core answer to skeptical AI infrastructure partners. SDN
turns an LLM suggestion into a series of concrete artifacts where each step can
be accepted or rejected locally.

## Verifiable Data Plane For Agents

The data-side proof chain should be documented just as explicitly:

```text
FlatBuffer record
  -> immutable shard or content-addressed object
  -> signed publication/log-head announcement
  -> PNM notification
  -> local fetch/pin policy
  -> FlatSQL materialized index
  -> subscription, query, or module invocation
```

This is what lets agents reason over current space data without treating model
weights as the source of truth.

## Council Lens For The Pitch

Use four expert lenses when reviewing any AI-first messaging.

### 1. Standards Council

Question: can an agent identify the canonical data contract without inventing a
local alias?

Required emphasis:

- schemas live upstream in `spacedatastandards.org`
- generated bindings are consumed downstream
- `PLG` is the canonical module marketplace/listing record
- plugin invoke primitives belong in SDS, not in a repo-local duplicate
- every schema change must explain generated-language impact

### 2. Module Council

Question: can an agent produce a portable module artifact and prove it complies?

Required emphasis:

- start from an example module
- declare the runtime target: `wasi`, `wasmedge`, or shared
  `browser` + `wasmedge`
- state whether the module uses direct invoke, command invoke, or both
- keep `dist/isomorphic/module.wasm` as the canonical shared artifact
- validate manifest round trip and standards-aware manifest validation
- validate wasm exports and artifact compliance
- test browser and WasmEdge portability when the target claims both
- sign, encrypt, or bundle through SDK helpers rather than custom bytes
- keep FlatBuffers binary on stream and ingest paths; do not route canonical
  FlatBuffer ingest through JSON or base64

### 3. Operator Council

Question: can an agent deploy and inspect a node without weakening security?

Required emphasis:

- Docker and bare-metal paths are first-class
- production configs must not reuse tracked dev wallet material
- full nodes and edge relays have different responsibilities
- edge relays move packets; full nodes carry state
- deployment is an observable change, not a chat command
- node identity, provider identity, EPM trust, and user admin access are separate
- module bundles stay encrypted in transit and at rest
- Tor-on-by-default is a privacy baseline; disabling it is a local-debug
  exception
- services should default toward hardened shapes: non-root `sdn`, systemd
  sandboxing where available, read-only containers, `no-new-privileges`, health
  checks, and explicit resource limits
- production deployments require explicit key and trust policy decisions

### 4. LLM Provider Council

Question: why should model labs, inference providers, and coding-agent platforms
care?

Required emphasis:

- SDN is a high-value benchmark for real agentic coding: multi-repo routing,
  FlatBuffers, WebAssembly, Go, TypeScript, Docker, crypto metadata, and browser
  validation
- SDN produces concrete artifacts that can be tested, not subjective text output
- signed SDS records and IPFS CIDs give agent systems provenance they can
  inspect, cite, route, and verify
- portable WASM modules provide deterministic tools next to probabilistic model
  calls
- AI/ML inference modules should use the same module, manifest, capability,
  signing, and delivery path as other modules; no privileged AI side channel
- the network can become a distribution path for AI-created but human-approved
  space-data modules
- provider support can be framed as infrastructure for open scientific and
  operational data exchange, not as another generic code assistant demo

## Message To LLM Creators And Inference Providers

Use this framing when pitching foundation model labs, inference providers, IDE
agent vendors, and coding-platform teams:

> SDN gives AI coding agents a real, open, safety-conscious infrastructure
> workload. An agent must understand schemas, generate or modify code, compile
> WebAssembly, validate binary artifacts, package and sign modules, deploy
> network nodes, and verify browser/server portability. The result is measurable:
> a passing compliance check, a running node, a signed bundle, a published schema,
> or an OrbPro demo.

For model and inference teams building operational AI products, add:

> SDN is a live data and tool plane for space-aware AI. It gives models typed
> FlatBuffer records instead of ambiguous scraped text, signed publications
> instead of unattributed claims, and deterministic WASM tools for calculations
> that should not be improvised by a language model.

Why this matters to them:

1. **It is a serious agent benchmark.** SDN is not a toy app. It exercises
   multi-language, multi-repo, artifact-producing software work.
2. **It has objective grading.** A model either builds the module, validates the
   manifest, runs the node, passes tests, or it does not.
3. **It is domain-specific but open.** Space data is technical enough to show
   model capability, while the contracts can be public, testable, and reusable.
4. **It creates useful demand for inference.** Module authors, node operators,
   researchers, and integrators can use agents repeatedly for real work.
5. **It rewards safe tool use.** Agents must follow validation gates, respect key
   boundaries, and avoid inventing unsupported formats.
6. **It supports local and hosted inference.** The workflow should work with
   cloud agents, enterprise inference endpoints, and local models in restricted
   environments.
7. **It improves provenance-aware reasoning.** Agent outputs can point back to
   signed SDS records, content-addressed artifacts, and auditable tool traces.

Concrete partnership asks:

- Add SDN task packets to coding-agent eval suites.
- Sponsor or provide credits for an "AI-built SDN module" challenge.
- Run SDN nodes close to inference clusters for low-latency access to signed
  space-data streams.
- Build SDS/SDN adapters for model tool-calling APIs and agent frameworks.
- Help build retrieval packs for `spacedatastandards.org`,
  `space-data-module-sdk`, `space-data-network`, and OrbPro docs.
- Provide hosted inference grants for open-source module authors and node
  operators.
- Integrate SDN golden paths into agent IDE examples.
- Collaborate on safe-agent templates for cryptographic, deployment, and
  scientific-computing workflows.
- Co-design eval sets for space-data reasoning, provenance use, and tool
  selection.
- Support FlatBuffers/SDS ingest paths directly instead of forcing JSON-only
  bridges.

Language to use:

- "verifiable agentic coding"
- "artifact-producing workflows"
- "contract-first infrastructure"
- "AI-operable open network"
- "domain-specific coding benchmark"
- "human-approved, machine-validated modules"
- "provider-neutral agent task packets"
- "deterministic WASM tools beside probabilistic models"
- "provenance-aware inference"
- "live data access for AI systems"
- "from prompt to proof"
- "the trust boundary is not the model"
- "inference is just another module"

Language to avoid:

- "autonomous mission operations"
- "AI-certified modules"
- "self-deploying critical infrastructure"
- "trust the model"
- "no-code space software"
- "AGI for space"
- "autonomous collision avoidance"
- "solves hallucinations"
- "truth layer"
- "training data goldmine"

## Public Documentation Structure

Add a visible agentic documentation spine. Hidden `.claude`, `AGENTS.md`, and
tool-specific files are useful, but public AI-first adoption needs normal
markdown that any LLM can ingest.

### `space-data-network`

Create these files under `docs/agentic/`:

- `README.md`: start here for AI coding agents
- `architecture-map.md`: four-repo ownership and routing
- `task-index.md`: choose the correct task packet
- `deploy-node-docker.md`: local cluster, full node, edge relay
- `deploy-node-bare-metal.md`: Linux VM/systemd path
- `publish-module-provider.md`: provider discovery and encrypted delivery
- `operate-node.md`: identity, trust, logs, health, updates
- `verification-matrix.md`: command list by changed surface

Keep this root file, `SDN-AI.md`, as the strategy and pitch document.

### `space-data-module-sdk`

Create these files under `docs/agentic/`:

- `create-module.md`
- `manifest-checklist.md`
- `compile-validate-bundle-sign.md`
- `browser-wasmedge-portability.md`
- `module-publication.md`

Add one minimal module example designed for agents:

- `examples/agentic-basic-module/`

### `spacedatastandards.org`

Create these files under `docs/agentic/`:

- `add-schema.md`
- `schema-rules.md`
- `publish-bindings.md`
- `consume-bindings-downstream.md`

### `OrbPro`

Create these files under `docs/agentic/`:

- `load-sdn-module.md`
- `create-orbpro-plugin-demo.md`
- `sandcastle-validation.md`
- `runtime-invariants.md`

## Standard Agent Task Packet

Every agent-facing guide should use this exact shape:

```md
# Task: [Outcome]

Intent:
What the user is trying to accomplish.

Owning repository:
The only repo where this task should make primary changes.

Related repositories:
Repos the agent may read or update only when the packet says so.

Do not edit:
Explicit boundaries that prevent architectural drift.

Prerequisites:
Tools, packages, credentials, keys, environment variables, and local services.

Inputs the agent needs:
Manifest path, schema name, node type, domain, CID, key path, or deployment target.

Files to create or modify:
Exact paths.

Commands:
Copyable commands from repo root.

Expected artifacts:
WASM file, bundle, schema binding, Docker container, systemd service, screenshot,
or validation report.

Validation:
Commands that must pass before the agent can claim completion.

Failure handling:
What to do when a command fails, a key is missing, or a contract is violated.

Operator approval gates:
Actions the agent may prepare but must not apply without explicit approval,
including trust-list changes, admin/API public exposure, Tor disablement,
identity rotation, plugin installation, firewall changes, paid entitlement
changes, TLS private key changes, and production deployment.

Definition of done:
The smallest concrete proof that the task is finished.
```

Task packets should be written for an agent with no prior context. They should
not rely on hidden memory, local conventions, or implied knowledge.

Each task packet should also include a short failure-mode block when the task
touches modules, schemas, delivery, or deployment:

- Do not invent local schema names, generated bindings, `PLG` variants, or
  bundle trailers.
- Do not call SHA-256 hex digests CIDs.
- Do not confuse data storefront `STF` records with module listing `PLG`
  records.
- Do not claim browser/WasmEdge portability when hostcalls, pthreads, sockets,
  TLS imports, or diagnostics-to-stdout break the target surface.
- Do not replace direct browser module delivery with a helper service or broker.
- Do not put mnemonics, private keys, xprivs, or unwrapped content keys into
  prompts, fixtures, or commits.
- Do not claim signatures prove data truth; they prove authorship and integrity.
- Do not claim arbitrary computation over encrypted data; homomorphic work is a
  narrow governed extension track, not a general SQL-over-ciphertext promise.

## Golden Paths

The AI-first launch should be organized around five reproducible workflows.

### 1. Prompt To Compliant Module

User asks:

> Create an SDN module that accepts OMM and emits propagated state vectors.

Agent path:

1. Read `space-data-module-sdk/docs/agentic/create-module.md`.
2. Start from the closest SDK example.
3. Use canonical SDS schema names and file identifiers.
4. Compile to `dist/isomorphic/module.wasm`.
5. Validate manifest, standards, and wasm artifact.
6. Bundle, sign, or encrypt if requested.
7. Run browser/WasmEdge portability checks if claimed.

Proof:

```bash
npm test
npm run check:compliance
npx space-data-module check --manifest ./manifest.json --wasm ./dist/isomorphic/module.wasm
```

### 2. Prompt To Local SDN Cluster

User asks:

> Start a local SDN network with full nodes and edge relays.

Agent path:

1. Read `space-data-network/docs/agentic/deploy-node-docker.md`.
2. Use `deployment/scripts/local-cluster.sh`.
3. Verify full nodes, edge relays, and registry builder.
4. Report port mappings and health status.

Proof:

```bash
deployment/scripts/local-cluster.sh up -d
deployment/scripts/local-cluster.sh status
deployment/scripts/local-cluster.sh test
```

### 3. Prompt To Production Node

User asks:

> Deploy a full SDN node on a Linux server.

Agent path:

1. Read `space-data-network/docs/agentic/deploy-node-bare-metal.md`.
2. Confirm deployment target, node type, and key policy.
3. Refuse production deployment if tracked dev wallet material is present.
4. Classify and redact secrets: mnemonics, Space-Track credentials, Stripe keys,
   admin tokens, TLS private keys, `SDN_KEY_PASSWORD`, and unwrapped content
   keys.
5. Use Docker or binary/systemd path after explicit operator approval.
6. Verify service, admin API, peer ID, signed EPM identity, DHT/provider
   descriptor, and logs.
7. Record maintenance state: upgrade path, rollback path, backup location,
   secret rotation procedure, and capacity limits.

Proof:

```bash
deployment/scripts/deploy.sh --dry-run deploy full
deployment/scripts/deploy.sh deploy full
systemctl status spacedatanetwork
```

### 4. Prompt To Schema Release

User asks:

> Add a field to the module listing schema.

Agent path:

1. Route to `spacedatastandards.org`.
2. Read schema naming and collision rules.
3. Modify canonical FlatBuffer schema.
4. Build generated bindings.
5. Publish or pin the new package version.
6. Consume the published version in SDK and SDN.

Proof:

```bash
npm run build
npm test
npm run check:versions
```

### 5. Prompt To OrbPro Integration

User asks:

> Load this SDN module in OrbPro and show it in a Sandcastle demo.

Agent path:

1. Route runtime and scene work to OrbPro.
2. Keep module compliance in SDK.
3. Use the unified plugin/module loading path.
4. Validate sampled Cesium-visible outputs.
5. Capture Sandcastle screenshot or browser verification.

Proof:

```bash
npm run check:sdn-plugin-compliance
npm run build
npm run test-sandcastle-gallery
```

## Backing Structure Required Before A Major AI-First Launch

The pitch needs evidence. Build these in order:

1. `SDN-AI.md` strategy document in `space-data-network`.
2. Public `docs/agentic/` hub in `space-data-network`.
3. One complete module-authoring task packet in `space-data-module-sdk`.
4. One complete Docker deployment task packet in `space-data-network`.
5. One complete bare-metal deployment task packet in `space-data-network`.
6. One complete schema-update task packet in `spacedatastandards.org`.
7. One complete OrbPro module-loading task packet in `OrbPro`.
8. A short "LLM provider brief" extracted from this file.
9. A smoke-test script that checks every documented command still exists.
10. A public demo video or recorded terminal session:
    prompt -> module -> compliance -> local node -> delivery -> OrbPro load.

## Suggested Website Copy

Short version:

> Build, validate, deploy, and operate space-data modules with AI coding agents.
> SDN gives agents the contracts they need and gives humans the verification
> gates they require.

Long version:

> Space Data Network is built for agentic software development from the ground
> up. Its schemas are canonical FlatBuffers, its analytics ship as portable
> WebAssembly modules, its network runs through verifiable node deployments, and
> its marketplace records are signed standards-backed artifacts. AI coding
> agents can generate useful work because every step has a contract: what to
> edit, what to build, what to validate, and what artifact proves the result.

Provider-facing version:

> SDN is a real-world agentic coding workload for LLM platforms. It combines
> schema evolution, WebAssembly compilation, cryptographic packaging,
> peer-to-peer deployment, browser/runtime portability, and domain-specific
> space data. Each task has objective grading through build, compliance, test,
> and deployment checks.

AI-infrastructure version:

> Space-aware models need more than text context. They need current orbital
> data, typed schemas, provenance, and deterministic tools. SDN supplies that
> substrate: live signed SDS records over a peer-to-peer network, portable WASM
> modules for repeatable computation, and local verification gates for every
> generated artifact.

Operator-facing version:

> Use an AI agent to prepare node deployment steps, inspect configuration, run
> health checks, and document operational state. Keep production authority with
> the operator: keys, trust policy, public exposure, and paid module access must
> remain explicit human decisions.

Infrastructure-safe version:

> AI can prepare a deployment; the operator authorizes trust. SDN runbooks should
> follow plan, diff, preflight, approve, apply, and verify. Identity,
> entitlement, publishing, firewall exposure, and production key changes fail
> closed until a human operator approves them.

Module-author version:

> Describe the module you want, then let the agent scaffold the manifest, source,
> build, compliance checks, bundle, and publication metadata. The SDK decides
> whether the result is compliant.

## Support Strategy

### For LLM labs

Ask for:

- SDN inclusion in coding-agent benchmark suites
- model-context/retrieval support for the four-repo documentation set
- credits for open-source module-authoring experiments
- joint writeups on verifiable agentic coding in scientific infrastructure

Offer:

- objective tasks with real build artifacts
- multi-language repo surfaces
- domain-specific but public contracts
- measurable failure modes

### For inference providers

Ask for:

- sponsored inference endpoints for SDN module authors and node operators
- examples showing SDN workflows on their hosted coding-agent stack
- support for private enterprise deployments where data and keys stay local

Offer:

- recurring, useful agent workloads
- clear demand from developers, operators, and researchers
- a non-consumer, infrastructure-grade proof point

### For IDE and agent-tool vendors

Ask for:

- SDN task-packet templates in their examples
- repo-routing support across sibling repos
- validation-aware completion summaries

Offer:

- end-to-end demos that produce real artifacts
- clear before/after value for developer productivity
- strong showcase for tool-using agents

## Guardrails For Credibility

The fastest way to lose support is to overclaim. Keep these guardrails in every
AI-first document:

- AI agents assist; compliance tools decide.
- Generated modules are not trusted until validated, signed, and reviewed.
- Production nodes are not deployed without explicit operator approval.
- Private keys, mnemonics, and unwrapped content keys are never pasted into
  prompts or committed to repos.
- Secrets never enter prompts, logs, tickets, screenshots, generated examples,
  or committed fixtures.
- Schema changes start upstream in `spacedatastandards.org`.
- SDS records stay typed and binary on canonical ingest/streaming paths; JSON is
  a debugging or edge representation, not the binary contract.
- Module delivery uses canonical `PLG` and encrypted bundle flows.
- Browser helper services and legacy broker paths are not part of the current
  browser contract.
- OrbPro remains the application host; SDK and SDN remain the module/network
  contract owners.
- Browser peers can publish, subscribe, verify, and use delivered modules, but
  reachable full nodes own DHT routing, relay, and durable network backbone
  responsibilities.
- Do not use exact schema counts unless they are generated from the pinned suite
  version.
- Do not say "no license servers" when provider/grant flows exist. Say the
  current browser delivery path does not require legacy helper services or
  broker flows.

## What AI Does Not Decide

The AI-first story is stronger when it names the boundary clearly.

AI agents may prepare options, diffs, commands, docs, validation runs, and
operator summaries. They do not decide:

- production key generation, custody, or rotation
- trust-list membership
- EPM trust policy
- paid access and entitlement changes
- public admin/API exposure
- firewall exposure
- disabling privacy defaults such as Tor
- canonical schema changes without upstream review
- publication of signed data, modules, or provider claims
- operational execution in high-consequence workflows

## Immediate Next Steps

1. Review this strategy with technical, operator, and provider audiences.
2. Create `docs/agentic/README.md` in `space-data-network`.
3. Create the first two task packets:
   - `docs/agentic/deploy-node-docker.md`
   - `docs/agentic/publish-module-provider.md`
4. Add matching task packets in `space-data-module-sdk` for module creation and
   compliance.
5. Extract a one-page `docs/agentic/llm-provider-brief.md`.
6. Record a demo where an agent builds a small module, validates it, starts a
   local SDN cluster, and loads the module through the SDK harness.

## One-Sentence North Star

SDN should be the network where AI agents can build space-data software because
the contracts are open, the artifacts are portable, and the verification is
local, explicit, and repeatable.

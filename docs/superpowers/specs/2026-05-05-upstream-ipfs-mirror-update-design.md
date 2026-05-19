# Upstream IPFS Mirror Update Design

## Goal

Keep upstream IPFS WebUI and IPFS Desktop architecture intact while allowing SDN
to carry product-specific UI, routing, branding, and deployment behavior in an
explicit overlay layer.

## Architecture

`webui/` and `desktop/` are treated as upstream mirrors. SDN-specific behavior
must not be added directly to those trees unless it is a temporary migration
step documented in the same change. Long-lived SDN behavior belongs in
`sdn-js/ui/src/upstream-webui/overrides/`, generated vendor snapshots under
`sdn-js/ui/src/upstream-webui/vendor/`, or explicit patch/adapter files that can
be reapplied after an upstream refresh.

The update process should be repeatable:

1. Fetch the selected upstream IPFS WebUI and IPFS Desktop revisions.
2. Refresh the mirror trees.
3. Refresh generated vendor snapshots consumed by SDN overrides.
4. Apply SDN overlays or patch files.
5. Run boundary tests that prove upstream mirrors are clean and SDN routes still
   mount `/`, `/webui`, and `/admin` separately.
6. Build the SDN UI and desktop surface before deploy.

## Rules

- Do not introduce SDN product architecture into upstream mirror files.
- Do not require Kubo CORS allowlist entries for SDN-only custom schemes such
  as `webui://-` or `sdn://-`.
- Prefer upstream-compatible HTTP origins and upstream desktop loading patterns
  for IPFS WebUI RPC access.
- Any direct edit inside `webui/` or `desktop/` must either be an upstream
  mirror refresh or be moved into an overlay/patch before the work is complete.
- Upstream refresh commits must include the upstream revision, overlay refresh,
  and focused contract tests in the handoff.

## Verification

Minimum verification for upstream IPFS update work:

```bash
node scripts/sync-upstream-webui-into-sdn-js.mjs --check
npm --prefix sdn-js run build:ui
npm --prefix sdn-js exec vitest run src/ui/upstream-webui/branding.test.ts
```

When desktop behavior changes, also run the desktop unit tests and launch the
desktop app long enough to verify that its IPFS WebUI reaches Kubo without
custom SDN-only CORS origins.

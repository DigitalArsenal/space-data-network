# OrbPro Third-Party Schema Policy

This document defines compatibility and versioning rules for OrbPro third-party
plugin/client schemas maintained in `packages/plugin-sdk/schemas/orbpro`.

## Source of truth

- All authoritative OrbPro plugin schemas live in:
  - `packages/plugin-sdk/schemas/orbpro/key-broker/`
  - `packages/plugin-sdk/schemas/orbpro/third-party/v1/`
- Client, server, and tooling repositories must not maintain forked schema copies.

## Compatibility model

- Minor, non-breaking changes:
  - Add optional fields
  - Add enum values only when old parsers can safely ignore unknown values
- Breaking changes:
  - Remove fields
  - Change field type/semantics
  - Reassign file identifiers
  - Reorder or repurpose required fields

## Versioning rules

- Foldered major versioning is required:
  - `.../third-party/v1/`
  - future breaking release must use `.../third-party/v2/`
- File identifiers are immutable per message type.
- Generated bindings and fixtures must be regenerated and committed with schema updates.

## Deprecation windows

- Existing major versions remain supported for one full release cycle minimum.
- Removal of a major version requires:
  1. Migration guide in docs
  2. Conformance fixtures for the replacement major version
  3. CI updated to validate replacement flows

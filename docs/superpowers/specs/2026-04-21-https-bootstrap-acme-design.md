# HTTPS Bootstrap And Managed ACME Design

Date: 2026-04-21
Status: Draft for review

## Summary

`sdn-server` will enforce HTTPS for all browser-facing product surfaces and will manage two TLS phases:

1. `bootstrap` TLS: a persisted self-signed node certificate served immediately on first boot and in local development
2. `managed` TLS: an ACME-issued public certificate for explicitly configured hostnames after DNS has been pointed at the node

The bootstrap certificate carries an SDN-specific non-critical X.509 extension that binds the TLS certificate public key to the node's HD-wallet-derived identity material. The login page displays this proof directly, along with a download link for the bootstrap certificate so operators can install or trust it locally.

The first ACME implementation supports explicit hostnames only. Reverse DNS is never used as authority. DNS-provider automation is out of scope for this slice.

## Problem

The current server can serve native TLS only when the operator manually provides a static certificate and key file. That blocks a clean product flow:

- a fresh node cannot reliably open a secure GUI on first boot
- local development still falls back to insecure HTTP
- users cannot configure hostname-based public TLS from inside the app itself
- the server does not provide a first-boot identity proof that binds a self-signed TLS endpoint to the node's wallet-derived identity

We need a TLS model that:

- makes the GUI reachable over HTTPS from first boot
- works in local dev
- allows the operator to configure an explicit hostname inside the app
- automatically upgrades to a public ACME certificate once DNS is correctly pointed at the node
- keeps `/`, `/webui`, and `/admin` under the same HTTPS identity without changing their routing contract

## Goals

- Enforce HTTPS for server-hosted UI and auth flows.
- Generate and persist a bootstrap self-signed certificate when no public certificate is configured.
- Embed a machine-verifiable SDN identity proof in a custom X.509 extension.
- Show the bootstrap proof directly on `/login`.
- Provide a downloadable PEM/CRT version of the bootstrap certificate for local trust installation.
- Support explicit-hostname ACME issuance and renewal from inside `sdn-server`.
- Keep HTTP on port `80` only for ACME challenge handling and redirect-to-HTTPS.
- Keep `/` as the SDN UI, `/webui` as the upstream IPFS WebUI, and `/admin` as admin/auth.

## Non-Goals

- Reverse-DNS-based hostname discovery or issuance.
- Automatic DNS provider integration.
- Automatic issuance for arbitrary PTR names or guessed domains.
- Let’s Encrypt `localhost` certificates.
- Public-IP ACME certificates in this slice.
- Replacing the existing static-cert mode.
- Replacing wallet authentication with certificate-based user authentication.

## Terminology

- `bootstrap certificate`: the node-local self-signed certificate used before ACME issuance exists
- `managed certificate`: the ACME-issued certificate currently active for one or more configured hostnames
- `bootstrap proof`: the SDN-specific X.509 extension payload that binds the TLS certificate public key to the node's HD-wallet-derived identity material

## TLS Modes

The admin/server TLS configuration will move from a single `tls_enabled` boolean to an explicit mode.

- `disabled`
  - current HTTP behavior
  - allowed for tests only
  - not recommended for browser-facing deployments
- `static`
  - native HTTPS using operator-supplied `tls_cert_file` and `tls_key_file`
  - current manual deployment mode
- `managed`
  - native HTTPS always on
  - bootstrap self-signed cert is generated and served immediately
  - ACME-managed cert replaces bootstrap cert after successful hostname issuance

`managed` is the intended default for product and dev flows.

## Certificate Lifecycle

### Bootstrap Certificate

When `tls_mode=managed` starts and no valid managed certificate is available, the daemon generates and persists a bootstrap certificate and matching private key under writable node storage.

Bootstrap certificate requirements:

- self-signed
- persisted across restarts
- stable until explicit reset or corruption recovery
- valid for 5 years
- SANs include:
  - `localhost`
  - `127.0.0.1`
  - `::1`
  - the host portion of `admin.listen_addr` when it is an IP literal or concrete hostname rather than `0.0.0.0` or `::`
- key algorithm:
  - ECDSA P-256
- subject:
  - `CN=Space Data Network Node`
  - `O=Space Data Network`
  - `OU=Bootstrap TLS`

The daemon does not silently rotate the bootstrap certificate on every start. Stability matters because operators may import the certificate into the local trust store. Rotation occurs only when:

- no bootstrap certificate exists
- the bootstrap certificate and private key do not match
- the bootstrap certificate is unreadable
- the operator explicitly requests bootstrap certificate reset

### Managed ACME Certificate

Once the operator configures one or more explicit hostnames and the daemon successfully completes ACME validation, the managed certificate becomes the active certificate presented by the server.

Managed certificate behavior:

- only names explicitly saved in config are eligible
- ACME cache and account material live in writable node storage
- HTTP-01 is the default challenge flow
- port `80` remains open for challenge responses and redirect-to-HTTPS
- on successful issuance, the managed certificate replaces the bootstrap certificate at handshake time
- on renewal failure, the daemon keeps serving the last valid managed certificate until expiry
- the daemon must never silently fall back from an active managed certificate to a newly generated bootstrap identity

If the managed certificate expires and cannot be renewed, the daemon enters a degraded TLS state:

- keep serving the expired managed certificate
- show clear UI warnings on `/login`
- require explicit operator action to reset back to bootstrap identity

This avoids changing the TLS identity behind the operator's back.

## Bootstrap Proof Extension

The bootstrap certificate carries a non-critical SDN custom X.509 extension.

OID:

- `1.3.112.4.57.10.1`

This extension is an SDN product-local extension under the existing SDS OID arc used in this repo.

### Payload

The extension payload is DER-encoded ASN.1 with this shape:

```asn1
SDNBootstrapBinding ::= SEQUENCE {
  version INTEGER (1),
  peerId UTF8String OPTIONAL,
  encryptionPath UTF8String,
  encryptionX25519PublicKey OCTET STRING,
  encryptionProofEd25519PublicKey OCTET STRING,
  tlsSubjectPublicKeyInfoSha256 OCTET STRING,
  signatureAlgorithm OBJECT IDENTIFIER,
  signature OCTET STRING
}
```

Field definitions:

- `version`
  - fixed to `1`
- `peerId`
  - optional libp2p peer ID string when available
- `encryptionPath`
  - the HD derivation path used for node encryption proof material
  - default: `m/44'/0'/0'/1'/0'`
- `encryptionX25519PublicKey`
  - the node's published X25519 encryption public key for that path
- `encryptionProofEd25519PublicKey`
  - the Ed25519 public key derived from the same encryption path seed material, used only to verify the bootstrap proof signature
- `tlsSubjectPublicKeyInfoSha256`
  - SHA-256 over the certificate leaf `SubjectPublicKeyInfo`
- `signatureAlgorithm`
  - Ed25519 OID `1.3.101.112`
- `signature`
  - Ed25519 signature over:
    - ASCII domain separator `SDN TLS BOOTSTRAP BINDING V1`
    - `tlsSubjectPublicKeyInfoSha256`
    - `peerId` if present
    - `encryptionPath`
    - `encryptionX25519PublicKey`
    - `encryptionProofEd25519PublicKey`

### Why this shape

The node's published encryption key is X25519, which is used for key agreement and cannot directly sign. The proof therefore uses Ed25519 key material derived from the same HD encryption path seed material. This satisfies the product requirement that the proof be anchored to the node's encryption path while remaining cryptographically valid and implementable.

The Subject DN remains human-readable. The node-proof data lives in the custom extension rather than being stuffed into DN fields.

## HTTPS Serving Rules

When native TLS is active (`static` or `managed`):

- the primary browser surfaces are available only on HTTPS:
  - `/`
  - `/login`
  - `/admin`
  - `/webui`
  - authenticated API routes
- a companion HTTP listener on port `80` does only two things:
  - serves `/.well-known/acme-challenge/*`
  - redirects every other request to the equivalent HTTPS URL with `308 Permanent Redirect`

When native TLS is not active:

- current HTTP behavior remains available only for tests or explicitly insecure configurations

## Cookie And Transport Policy

When native TLS is active:

- session cookies are always `Secure`
- session cookies remain `HttpOnly`
- session cookies remain `SameSite=Lax`
- CSRF checks continue to apply

HSTS policy:

- do not send HSTS while serving the bootstrap self-signed certificate
- send HSTS only when serving a valid managed ACME certificate
- header value:
  - `Strict-Transport-Security: max-age=63072000; includeSubDomains`

Reason:

- HSTS during the bootstrap self-signed phase would create an unnecessarily sticky failure mode if the operator is still onboarding or using a certificate that is not yet locally trusted

## Hostname Enrollment Flow

The first cut supports explicit hostname enrollment only.

### Operator Flow

1. Operator reaches the daemon over HTTPS using the bootstrap self-signed certificate.
2. Operator clicks through the browser warning or installs the bootstrap certificate locally.
3. Operator opens the `TLS / Hostname` settings surface.
4. Operator enters one or more hostnames.
5. UI instructs the operator to manually point `A`/`AAAA` records at the node.
6. Daemon performs preflight checks.
7. Operator triggers issuance.
8. Daemon attempts ACME HTTP-01 issuance for the exact configured names.
9. On success, the daemon begins presenting the managed certificate.

### Preflight Checks

Before starting ACME issuance, the daemon validates:

- each configured name is a hostname, not `localhost` and not an IP literal
- the hostname resolves via the system resolver
- the HTTP listener required for `HTTP-01` is enabled on port `80`
- the HTTPS listener required for the application is enabled on port `443`

Preflight does not claim Internet reachability from an external vantage point. It is only a local sanity check. Final authority is the ACME validation result.

### ACME Host Policy

ACME issuance is restricted to the saved hostname list only.

- no on-demand issuance for arbitrary SNI names
- no reverse-DNS inference
- no wildcard issuance in this slice
- no DNS-01 flow in this slice

## Login Page UX

The login page gains a TLS identity block rendered directly on the page.

The block shows:

- current TLS mode: `bootstrap`, `managed`, or `static`
- certificate type:
  - `Bootstrap self-signed`
  - `Managed public certificate`
  - `Static operator certificate`
- certificate fingerprint:
  - SHA-256 fingerprint of the presented leaf cert
- validity window
- hostname list currently covered by the active cert
- node peer ID when available
- node encryption X25519 public key
- proof verification status

When serving the bootstrap certificate, the page also shows:

- a short explanation that the first-boot certificate is self-signed
- a download link for PEM/CRT installation
- a short explanation that the custom extension binds the TLS key to the node's HD-wallet-derived identity

When serving a managed ACME certificate, the page still shows TLS status, but the bootstrap download control is hidden.

## Downloadable Bootstrap Certificate

The daemon exposes a public download endpoint for the active bootstrap certificate:

- `GET /bootstrap.crt`

Behavior:

- returns the PEM-encoded bootstrap certificate
- available only when a bootstrap certificate exists
- if the active cert is managed ACME and the bootstrap cert still exists on disk, the endpoint may still return the bootstrap PEM for local trust workflows, but the login page should label it clearly as `bootstrap-only, not currently active`

## Configuration Shape

`admin` config expands as follows:

```yaml
admin:
  listen_addr: "0.0.0.0:443"
  http_challenge_addr: "0.0.0.0:80"
  require_auth: true
  tls_mode: managed
  tls_cache_dir: "/var/lib/spacedatanetwork/tls"
  tls_hosts:
    - "sdn.spaceaware.io"
  tls_cert_file: ""
  tls_key_file: ""
```

Rules:

- `tls_mode=disabled`
  - ignore `tls_hosts`
- `tls_mode=static`
  - require `tls_cert_file` and `tls_key_file`
- `tls_mode=managed`
  - ignore static cert/key fields
  - require writable `tls_cache_dir`
  - allow empty `tls_hosts` on first boot so bootstrap TLS still works before hostname enrollment

Backward compatibility:

- if `tls_enabled=true` and static cert/key files are set, interpret as `tls_mode=static`
- if `tls_enabled=true` and static cert/key files are empty, interpret as `tls_mode=managed`

## Storage Layout

All managed TLS material lives under `tls_cache_dir`.

Required files:

- `bootstrap-cert.pem`
- `bootstrap-key.pem`
- `acme-account/`
- `acme-cache/`
- `managed-hosts.json`

The bootstrap certificate and key are separate from ACME account/cert material so reset and diagnostics remain simple.

## Route Contract

This design does not change product routes:

- `/` remains the SDN UI
- `/webui` remains the upstream IPFS WebUI
- `/admin` remains the admin/auth namespace

All three routes are served under the same active HTTPS identity.

## Implementation Boundaries

### New `sdn-server` TLS package

Add a dedicated internal package for:

- bootstrap certificate generation and persistence
- bootstrap proof extension encoding and verification
- managed certificate selection
- ACME manager setup and cache ownership
- active TLS status reporting

### `cmd/spacedatanetwork/main.go`

Refactor server startup so:

- TLS mode is selected from config
- managed mode wires:
  - HTTPS server on the main admin/app listener
  - HTTP challenge + redirect server on port `80`
- certificate selection happens through `tls.Config.GetCertificate`
- static/manual mode still works

### `internal/auth/login_page.go`

Add server-rendered TLS status and bootstrap proof information directly to the login page.

### Admin settings UI

Add a `TLS / Hostname` settings surface that:

- shows the current active cert mode
- shows configured hostnames
- allows add/remove hostname
- explains manual DNS steps
- triggers issuance attempt
- shows last issuance/renewal result

## Failure Modes

### Bootstrap generation failure

- fail server startup
- log the specific certificate or key generation error

### ACME issuance failure

- keep serving bootstrap certificate if no managed certificate has ever succeeded
- keep serving current valid managed certificate if one already exists
- expose the failure reason in the TLS settings UI

### Managed cert expired and renewal failed

- continue serving the expired managed certificate
- render a prominent operator warning on `/login`
- require explicit reset to bootstrap TLS if the operator wants to abandon the managed identity

### Hostname mismatch during bootstrap access

If the operator accesses the server using a hostname or IP not covered by bootstrap SANs, the browser may show a name-mismatch warning in addition to the self-signed warning. This is acceptable for first-boot onboarding. The login page still provides the fingerprint and proof block after the operator proceeds.

## Testing Strategy

### Go unit tests

- bootstrap certificate generation persists and reloads
- bootstrap certificate SANs are populated correctly
- bootstrap proof extension encodes and decodes correctly
- proof verification fails when:
  - TLS public key fingerprint changes
  - signature bytes change
  - encryption proof public key changes
- managed hostname policy rejects non-configured names
- HTTP listener redirects non-challenge requests to HTTPS
- challenge path bypasses redirect
- secure-cookie behavior is enabled for native TLS
- HSTS is sent only while a managed public certificate is active

### Focused integration tests

- `tls_mode=managed` with no hosts serves bootstrap HTTPS and login page status
- adding hosts updates status and preflight diagnostics
- bootstrap certificate download endpoint returns PEM
- managed cert selection supersedes bootstrap cert when valid ACME material exists

### Browser verification

- first load over HTTPS with self-signed bootstrap certificate
- login page renders bootstrap proof panel
- bootstrap certificate download succeeds
- session survives refresh over HTTPS
- `/` and `/webui` remain authenticated under the same TLS identity

## Operational Notes

- Reverse DNS may be shown in the UI only as informational text.
- Reverse DNS must never auto-populate `tls_hosts`.
- Local dev uses the same bootstrap self-signed flow as first-boot product mode.
- The downloadable bootstrap certificate is the intended local-dev escape hatch for removing browser warnings.

## Rollout

Implement in this order:

1. config shape and TLS mode selection
2. bootstrap certificate generation and proof extension
3. HTTPS listener plus HTTP challenge/redirect listener
4. login page TLS status block and bootstrap certificate download
5. explicit-hostname ACME issuance and renewal
6. TLS/Hostname settings UI

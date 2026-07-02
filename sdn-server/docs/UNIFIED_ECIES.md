# SDN Unified ECIES v1 — one cross-runtime envelope

**Status:** proposal + in-progress implementation (2026-07-02).
**Goal:** ONE content-key / payload encryption scheme that Go, C++/WASM, and
JS all produce and consume byte-identically, replacing the three
non-interoperable formats that exist today.

## Why

Three envelopes coexist and cannot interoperate:

1. Go `internal/license/plugins.go` `BuildPluginKeyEnvelope` — single AES-GCM
   over a JSON `{alg, server_x25519_pubkey, nonce, ciphertext}`.
2. C++ `client-decrypt` `decrypt_legacy_envelope` — double AES-GCM over a
   bespoke JSON (`ephemeralPublicKeyHex`, `hkdfSaltB64`, `wrappedKeyB64` …).
3. SDS `$ENC` header + `$KMF`/`$REC` payload — the canonical delivery grant
   path (`decrypt_lgr_grant`, `wrap_content_key_for_requester`).

(3) is already the real, schema-defined path and is the only one grounded in
spacedatastandards. v1 adopts it as **the** scheme and deletes (1) and (2).

## The scheme

An ECIES envelope is an SDS record set:

- **`$ENC` header** (`spacedatastandards.org schema/ENC`, already deployed):
  - `KEY_EXCHANGE : {X25519, Secp256k1, P256}` — the ECDH curve.
  - `SYMMETRIC : {AES_256_CTR (0), AES_256_GCM (1)}` — CTR for FlatBuffer
    field-level encryption, GCM (raw byte value 1, not yet in the enum) for
    whole-payload bundle sealing.
  - `KEY_DERIVATION : HKDF_SHA256`.
  - `EPHEMERAL_PUBLIC_KEY` — 32 bytes (X25519) or 33 bytes compressed
    (secp256k1 / P256).
  - `NONCE_START` — 12-byte GCM IV / CTR nonce base.
  - `RECIPIENT_KEY_ID`, `CONTEXT` (domain separator), `SCHEMA_HASH`,
    `ROOT_TYPE`, `TIMESTAMP`.
- **`$KMF` payload** carrying the wrapped content key in `KEY_BYTES`.

### Crypto pipeline (identical across runtimes)

1. Ephemeral keypair on the recipient's curve (per `ENC.KEY_EXCHANGE`).
2. `Z = ECDH(eph_priv, recipient_pub)`.
   - **X25519**: RFC 7748 raw shared secret (32 bytes).
   - **secp256k1 / P256**: the **32-byte big-endian X coordinate** of the
     shared point (SEC1), **not hashed**. Confirmed: Go decred v4
     `GenerateSharedSecret` returns exactly the raw X (RFC 5903 §9 — "return
     x", not hashed), matching CryptoPP `ECDH<ECP>.Agree` and WebCrypto
     `deriveBits`. (Left-pad to 32 bytes when X has leading zero bytes.)
3. `K = HKDF-SHA256(ikm = Z, salt = ENC.RECIPIENT_KEY_ID | none,
   info = ENC.CONTEXT)`. This is the flatbuffers `DeriveSymmetricKey`
   contract (`encryption.h`), already implemented for all curves.
4. Symmetric:
   - **AES-256-CTR field-level**: the flatbuffers `EncryptVector` sub-KDF
     (`derive_field_key` / `derive_field_iv` over `K`) encrypts the KMF
     `KEY_BYTES` field in place — the existing grant path.
   - **AES-256-GCM whole-payload**: `[12-byte NONCE_START IV][ct||16-byte
     tag]`, AAD = the canonically re-encoded `$ENC` header bytes — the
     module-bundle path.

Every step after the ECDH is already curve-agnostic and shared via
`flatbuffers/encryption.h` (which ships X25519/secp256k1/P256 ECDH, HKDF,
`DeriveSymmetricKey`, `EncryptVector`). So "add secp256k1" = branch the ECDH
on `ENC.KEY_EXCHANGE`; nothing downstream changes.

## Implementation plan

- **Go** `internal/ecies` (new): `Wrap`/`Unwrap` producing/consuming the
  `$ENC`+`$KMF` bytes for X25519 and secp256k1; the reference the other
  runtimes are validated against.
- **C++** `licensing/core` (wrap) + `client-decrypt` (unwrap): branch the
  hardcoded `CryptoPP::x25519` on `ENC.KEY_EXCHANGE` (add
  `CryptoPP::ECDH<ECP>(secp256k1)`, raw X). Delete `decrypt_legacy_envelope`.
- **SDK/wallet**: `KeyExchange` is already wired in `licensing/records.js`;
  add the secp256k1 wrap/unwrap using the wallet's secp256k1 ECDH.
- **Delete**: Go JSON `BuildPluginKeyEnvelope` / `plugins_secp256k1.go` JSON
  path; C++ `decrypt_legacy_envelope`. **DONE** — the Go JSON
  `PluginKeyEnvelope` (dead/test-only; the live wrap is the C++ licensing
  WASM) and C++ `decrypt_legacy_envelope` (modules `91ab0b1`) are both
  removed; the unified `$ENC`/`$KMF` scheme is the sole envelope across Go,
  JS, and C++.

## Cross-runtime conformance vectors

`internal/ecies/testdata/*.json`: `{keyExchange, recipientPriv,
recipientPub, ephemeralPriv, contentKey, encBytes, kmfBytes}`. Every runtime
must (a) unwrap the shared vector to `contentKey`, and (b) wrap for the
recipient such that another runtime unwraps it. X25519 vectors also validate
against the existing C++ `decrypt_lgr_grant`.

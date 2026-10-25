# Domain proofs

A domain proof is a statement signed by a publisher's EPM signing key that binds
one or more domain names to that publisher. A node that holds the EPM verifies
each proof. It lists every verified domain as evidence beside the publisher's
name. A proof never raises the publisher's rung (source-tag, signed, bonded).

Nothing in SDN issues proofs. The publisher's wallet or tooling signs the
statement below and stores the result in the EPM. The node checks the statement
and its signature; it does not look up DNS.

## Record

`EPM.DOMAIN_PROOFS` is a vector of `DomainProof` (SDS 1.266.0). One proof binds
every name in its `DOMAINS`.

| Field | Content |
| --- | --- |
| `DOMAINS` | `[string]`, one or more fully qualified names in statement order. Lowercase ASCII (`a-z`, `0-9`, `-`, `.`), internationalized names in punycode, no trailing dot, no empty label. Never empty. |
| `PUBLIC_KEY` | Lowercase hex, no `0x`. A signing key in the same EPM's `KEYS`. |
| `ALGORITHM` | `ed25519` (absent means `ed25519`) or `secp256k1`, the algorithm of that key. |
| `SIGNED_PAYLOAD` | Lowercase hex of the statement bytes. |
| `SIGNATURE` | Lowercase hex of the signature over the statement bytes. |

`KEY_PATH` and `ENCODING` are not read when verifying. The slot SDS 1.264.0 and
1.265.0 used for a single domain is retired and never read.

## Statement

The statement is ASCII. Every line, the last included, ends with LF (`0x0A`).
There is no CR, no space around `=` or `:`, and no blank line.

```text
sdn-domain-proof/2
domain=<name 1>
domain=<name 2>
...
key=<algorithm>:<public key hex>
peerid=<peer ID>
issued=<unix seconds>
expires=<unix seconds>
```

- One `domain=` line per element of `DOMAINS`, in the order of `DOMAINS`. At
  least one.
- `<algorithm>` is the proof's `ALGORITHM`, lowercase, spelled out even when the
  field is absent. `<public key hex>` is `PUBLIC_KEY`.
- `<peer ID>` is the first peer the EPM advertises in `MULTIFORMAT_ADDRESS`
  (`/p2p/<peer ID>` or `/ipns/<peer ID>`).
- `issued` and `expires` are decimal Unix seconds with no sign and no leading
  zero. `expires=0` means the proof does not expire.

A verifier rebuilds this text from the record and compares bytes. Any other form
of `SIGNED_PAYLOAD` is refused.

## Signature

- `ed25519`: Ed25519 over the statement bytes. `SIGNATURE` is the 64-byte
  signature as hex.
- `secp256k1`: ECDSA over the SHA-256 digest of the statement bytes. `SIGNATURE`
  is the DER encoding as hex. `PUBLIC_KEY` is the SEC1 public key as hex.

## Validity

A proof is valid from `issued` minus 300 seconds. It stays valid until `expires`
plus 300 seconds, or without end when `expires` is `0`.

## Refusal

A proof is refused as a whole, and adds no evidence for any of its names, when:

- `DOMAINS` is absent or empty. Proofs written under SDS 1.264.0 or 1.265.0
  decode with `DOMAINS` empty; issue them again.
- any name in `DOMAINS` is not canonical.
- `PUBLIC_KEY` is not a signing key of the same EPM with the same algorithm.
- `SIGNED_PAYLOAD` is not the statement above, byte for byte. An
  `sdn-domain-proof/1` statement, a missing, extra or reordered name, another
  peer ID and another key all fail here.
- the proof is outside its validity window.
- `SIGNATURE` does not verify.

## Test vector

An Ed25519 key signs a proof for two names.

| Item | Value |
| --- | --- |
| `DOMAINS` | `node.example.org`, `node.example.com` |
| `PUBLIC_KEY` | `03a107bff3ce10be1d70dd18e74bc09967e4d6309ba50d5f1ddc8664125531b8` |
| Peer ID | `12D3KooWExampleNodeForDomainProofs1111111111111` |
| `issued` | `1790000000` |
| `expires` | `1800000000` |

Statement:

```text
sdn-domain-proof/2
domain=node.example.org
domain=node.example.com
key=ed25519:03a107bff3ce10be1d70dd18e74bc09967e4d6309ba50d5f1ddc8664125531b8
peerid=12D3KooWExampleNodeForDomainProofs1111111111111
issued=1790000000
expires=1800000000
```

`SIGNED_PAYLOAD`:

```text
73646e2d646f6d61696e2d70726f6f662f320a646f6d61696e3d6e6f64652e6578616d706c652e6f72670a646f6d61696e3d6e6f64652e6578616d706c652e636f6d0a6b65793d656432353531393a303361313037626666336365313062653164373064643138653734626330393936376534643633303962613530643566316464633836363431323535333162380a7065657269643d313244334b6f6f574578616d706c654e6f6465466f72446f6d61696e50726f6f6673313131313131313131313131310a6973737565643d313739303030303030300a657870697265733d313830303030303030300a
```

`SIGNATURE`:

```text
27acb947ee522fb5538fc85c1318762d7de12aeee27c8504dbadf3350ecb9fb25fd5b444588b1254d38140903969a4a9b6f51142898ea526bfe986664712ea06
```

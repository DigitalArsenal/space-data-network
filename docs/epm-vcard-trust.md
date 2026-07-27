# Signed EPM and vCard Trust Model

Space Data Network EPMs are portable node and user identity records. They can be exchanged over the network, downloaded, exported as vCards, encoded in QR codes, and stored in ordinary contact applications.

Because those records are portable, the record itself must be tamper-evident. SDN uses two related signatures:

- The embedded EPM signature signs the portable identity content.
- The PNM signature signs the network announcement for the EPM CID.

## Embedded EPM Signature

An EPM contains `SIGNATURE` and `SIGNATURE_TIMESTAMP`.

The signature payload is deterministic canonical JSON built from the EPM fields, excluding only `SIGNATURE`. `SIGNATURE_TIMESTAMP` is included in the payload so it cannot be altered without invalidating the signature.

The signature is produced by the node or user Ed25519 signing key at:

```text
m/44'/0'/account'/0'/0'
```

Verification requires:

1. Decode the size-prefixed EPM FlatBuffer.
2. Rebuild the canonical signing payload from the EPM fields.
3. Find the Ed25519 signing key in `KEYS`.
4. Verify `SIGNATURE` against the canonical payload.

## vCard Export

SDN vCards include normal contact fields plus SDN extensions:

```text
X-SDN-EPM-B64
X-SDN-EPM-CID
X-SDN-EPM-SIGNATURE
X-SDN-EPM-SIGNATURE-TIMESTAMP
X-SDN-PEER-ID
X-SDN-DIRECTORY-KIND
```

**Superseded 2026-07-27 by owner directive.** This section previously read:
*"`X-SDN-EPM-B64` is the source of truth. It contains the complete signed EPM
bytes. Importers must prefer this embedded payload over mutable vCard fields
such as `FN` or `ORG`."* That is no longer what the node emits.

`X-SDN-EPM-B64` is **no longer emitted**, and neither is any key material
(`X-SIGNING-KEY`, `X-ENCRYPTION-KEY`, `X-PUBLIC-KEY`, and the
`signing.`/`encryption.` email aliases that carried the same bytes). The card
was carrying a full copy of a record that is already retrievable, plus public
keys that are already derivable — both redundant.

**The source of truth is now the record itself, addressed by CID.** A vCard
carries the *verification chain*, not the record:

| alias | carries |
| --- | --- |
| `…@xpub.spacedatanetwork.org` | the account extended public key |
| `…@sign.spacedatanetwork.org` | the signing key's derivation path (base64url) |
| `…@encrypt.spacedatanetwork.org` | the encryption key's derivation path (base64url) |
| `…@epmsig.spacedatanetwork.org` | the record's embedded signature |
| `…@epmts.spacedatanetwork.org` | the signature timestamp |
| `…@epmcid.spacedatanetwork.org` | the record's content identifier |

A verifier therefore: derives the secp256k1 key from **xpub + path**, fetches
the authoritative record by **CID**, and verifies the signature against the
fetched bytes. Mutable display fields such as `FN` or `ORG` are never
authoritative — that principle is unchanged; only the mechanism for obtaining
the authoritative values has moved from an embedded copy to a CID fetch, which
is strictly stronger (a CID-addressed fetch cannot be stale or substituted).

**Key material on the record vs. on the card.** The EPM **record**'s `KEYS[]`
still carries the ed25519 public key, and must: SLIP-10 ed25519 has no public
derivation, so that key cannot be derived from any xpub and removing it from the
record would make ed25519 signatures unverifiable. Only the **vCard surface**
dropped key bytes. A card is a contact card; the record is the record.

**Importers reading older cards:** the embedded-blob reader is retained, so
pre-directive and third-party cards that still carry `X-SDN-EPM-B64` continue to
be verified from it and continue to beat spoofed display fields.

## PNM Announcement

When an EPM changes, the node computes a new CID for the exact EPM bytes and emits a PNM:

```text
FILE_ID = EPM
CID = <raw CIDv1 sha2-256 of EPM bytes>
SIGNATURE = sign(CID)
SIGNATURE_TYPE = Ed25519
```

The PNM signature proves the node announced that CID. The embedded EPM signature proves the identity record itself has not been edited.

## Directory Import

Directory import follows this order:

1. If a vCard contains `X-SDN-EPM-B64`, decode that EPM.
2. Verify the embedded EPM signature.
3. Normalize directory fields from the signed EPM, not the vCard display fields.
4. Compute and store the EPM CID.
5. Store the record in the FlatSQL-backed directory.

Unsigned legacy vCards can still be parsed for compatibility, but signed embedded EPM payloads are the trusted path.

## Network Discovery

When a node fetches another node's EPM over the EPM exchange protocol, it verifies the embedded EPM signature before caching or indexing it. If the signed EPM advertises a peer ID, it must match the peer that supplied the EPM.

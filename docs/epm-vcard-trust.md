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

`X-SDN-EPM-B64` is the source of truth. It contains the complete signed EPM bytes. Importers must prefer this embedded payload over mutable vCard fields such as `FN` or `ORG`.

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

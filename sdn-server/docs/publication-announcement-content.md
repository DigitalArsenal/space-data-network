Each `list-published` row adds `ANNOUNCEMENT_CID`, the content identifier of its
signed PNM announcement. The existing `PNM_CID` column has the same value.
An empty value means no announcement content is recorded for that publication.

A consumer reads `ANNOUNCEMENT_CID`, then fetches
`GET /api/v1/data/records/PNM.fbs/{ANNOUNCEMENT_CID}` from the serving node.
The response contains the original size-prefixed PNM FlatBuffer bytes, with
`application/x-flatbuffers`; no announcement-source scan is needed. A missing
record returns 404 and is not evidence of a verified publication.

Verify the returned content identifier. Decode PNM `SIGNATURE` as hexadecimal,
require `SIGNATURE_TYPE` to be `Ed25519`, and verify with a separately trusted
provider public key. The signed payload is the bytes `SDN-DPM-PNM`, a zero byte,
the UTF-8 `FILE_ID`, a zero byte, and the UTF-8 manifest `CID`. Verify that this
manifest CID equals the listing's `MANIFEST_CID`; then verify the signed DPM and
the shard and index identifiers before applying their contents. The PNM
signature authenticates the manifest identity, not every field in the listing.

The additional column appears only in publication listings. Existing segment
columns, canonical feed-head signature bytes, protocol IDs, and the raw-record
endpoint are unchanged. Readers that ignore unknown columns retain their
existing behavior.

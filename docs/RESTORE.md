# Backup and restore

What to keep off the box, and how to bring a node back after the disk is
gone. Two halves: the identity (who this node is) and the data (what it
holds). The identity half is complete today. The data half recovers what the
network still serves, and the runbook is honest about the rest.

## What to back up, and when

| Item | Command | When |
|---|---|---|
| Node identity (mnemonic, sealed) | `spacedatanetwork key export --format backup > node-identity.backup` | once after `init`, and after any identity change |
| Kubo peer identity | `spacedatanetwork key escrow create --repo <kubo repo> > kubo-identity.escrow` | once, after Kubo first runs |
| Config | copy `config.yaml` (and the `users:` block it carries) | after every change |
| Archive manifest CIDs | the `X-SDN-Archive-CIDs` header of `GET /api/v1/archives`, or the ARCHIVE cards on the source page | after every archive |
| Records you must not lose | `ARCHIVE` on the lane (`POST /api/v1/archive`), and keep the manifest CID | on a schedule you choose |

The identity backup is password-encrypted. Keep it, the escrow record, the
config and the CID list somewhere that is not this machine. Never export
`--format mnemonic` for a backup: it is the whole identity in plain text.

## Restore the identity

On the new machine, install the same or a newer build (see INSTALL.md), then:

```sh
spacedatanetwork key import --format backup --force < node-identity.backup   # asks for the backup password
spacedatanetwork key escrow recover --repo <kubo repo> kubo-identity.escrow  # Kubo peer identity, if Kubo ran under this node
cp config.yaml /etc/space-data-network/config.yaml
spacedatanetwork show-identity                                                # the peer ID and keys match the old node
```

`key import` refuses to overwrite an existing identity without `--force`; a
fresh `init` on the new machine would have minted a new one, so import first
or pass `--force` knowingly. Trusted peers keep trusting the same keys, and
every record this node signed still verifies against them.

## Restore the data

### What comes back by itself

Records materialised from a trusted publisher come back when the node
reconnects: the publisher's current publications are announced again and the
node imports them. Keep the publisher in both `peers.trusted_peers` and
`network.bootstrap` in the restored config; the current set of each lane
(the one a replace-current subscription keeps) is rebuilt from the publisher.

### What needs the CID list

Archives are not announced to the network yet. Each one is a signed `$DPM`
manifest whose shard and index the archiving node pinned in its Kubo. To
bring an archive back:

```sh
# an operator session on the new node; the manifest CID is from your list
curl -X POST http://127.0.0.1:5001/api/v1/archive/import \
  -H 'Content-Type: application/vnd.sdn.flatbuffers.stream' \
  --data-binary @request.qrp        # a $QRP frame carrying CID=<manifest CID>
```

The node fetches the manifest and its assets through its Kubo and verifies
the provider signature, every asset CID and every SHA-256 before a record
lands. The bytes must still be reachable: from the network if the archiving
node (or any peer that pinned them) is online, or from another node's
archive plane at `GET /api/v1/archives/<manifestCid>/asset/<assetCid>`.

### What is lost

Records that were only on this node's disk and never published or archived.
There is no rebuild-from-network for them; archive what matters, and keep
the manifest CIDs off the box.

## Verify

```sh
spacedatanetwork status                         # daemon, data health, peers
curl -s http://127.0.0.1:5001/ready              # ready
curl -s http://127.0.0.1:5001/api/v1/stats       # record counts per standard
```

The SOURCES page shows every lane with its record count and last sync; a
lane that stays at zero after the publisher reconnects is the one to look at.

## Known gaps on this date

- Archives are not announced, so a wiped node cannot discover its own old
  archives without the CID list.
- There is no `restore --from-peer` verb; recovery is the subscription plus
  the import calls above.
- Kubo is not yet supervised by the node; restore Kubo (the escrow record
  covers its identity) before importing archives.

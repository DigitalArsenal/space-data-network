# TODO

- [ ] Create config system
  - [x] Create datastore in encrypted SQLite DB
  - [x] Allow user save / load keys
  - [ ] Allow user to save / load config
- [x] Create libp2p node, with bootstrap list
  - [x] Use all transports (tcp, websockets, etc)
  - [x] Use all discovery methods
  - [x] Use dht and identity services (in case bootstrap nodes are down / unreachable)
- [x] Export / Import private key
- [x] Publish PNM after pin
- [x] Republish IPNS
- [x] Publish folder with vcf, QR image, index.html (editable)
- [ ] Change folder interface to publish on standard channel after ingest (ex: Publish(version+OMM, data))
- [ ] Write to {standard}/{peerid} for data, add it then remove it to get hash, look in PNMs to find metadata (inc filename)
- [ ] Rolling limit for files in IPNS folder (size/number)
- [ ] Pin settings
  - [ ] Pin other EPM, only mandatory pin
  - [ ] Have pin interface for adding pins based on key, message type, size? number? how recent?
- [ ] Programs interface
  - [ ] List of keys in program
  - [ ] Set up folder structure, program then subfolder for keys
  - [ ] Have direct dial protocols for clients to get their encrypted files
- [ ] Remove musl-gcc dependency
- [x] Versioning with version string for Advertise

## Concept

- [x] Have user create EPM using CLI, use running daemon to publish to IPFS, get CID, write EPM / PNM to folder, publish to IPNS (dup publishing will be taken care of using DHT) with rolling limit acting as a cache, publish data directly to channel for standard
- [ ] Publish a JSON manifest of the current PNMs of actively hosted files.  This way, it's still discoverable over IPFS/IPNS and human / machine readable.
- [ ] Manifest Update Message schema(?) timestamp, digital signature of hash of manifest.
- [ ] Program Manifest

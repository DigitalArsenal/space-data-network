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
- [ ] Publish PNM after pin
- [ ] Republish IPNS
- [ ] Restart server if cannot find peer
- [ ] Publish folder with vcf, QR image, index.html (editable)
- [ ] Pin settings
  - [ ] Pin other EPM, only mandatory pin
  - [ ] Have pin interface for adding pins based on key, message type, size? number? how recent?
- [ ] Programs interface
  - [ ] List of keys in program
  - [ ] Set up folder structure, program then subfolder for keys
  - [ ] Have direct dial protocols for clients to get their encrypted files
- [ ] Remove musl-gcc dependency
- [ ] Self-updating binary
- [ ] Versioning with version string for Advertise

## Marketing

- [ ] Realtime
- [ ] Encrypted / Secure
- [ ] Point to point security (isolated)

# Concept

Posting the index.html to IPNS; due to slow propagation, have it cached at nodes.
Create protocols to list pinned files, and request them directly.
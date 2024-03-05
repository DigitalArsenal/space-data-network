# TODO

- [ ] Create config system
  - [x] Create datastore in encrypted SQLite DB
  - [x] Allow user save / load keys
  - [ ] Allow user to save / load config
- [ ] Create libp2p node, with bootstrap list
  - [x] Use all transports (tcp, websockets, etc)
  - [x] Use all discovery methods
  - [x] Use dht and identity services (in case bootstrap nodes are down / unreachable)
- [ ] Discovery other nodes in space data network
  - [x] Discoverable through 'space-data-network' DHT key
  - [ ] Exchange EPMs for servers
  - [ ] Have EPMs advertise services (need to modify for this)
- [ ] PubSub
  - [ ] Channels established using "peerid:{schema_id}", e.g. "16Uiu2HAm5zJW5YvLVQqUM5GPJ46p1Tswh3BgTUABhKPdECMG13m7:OMM"
  - [ ] Publish data to IPFS using Space Data Standards raw buffer
  - [ ] Digitally sign multiaddr using Ethereum
  - [ ] Multiaddr and ETH digital signature sent over channel using the Publish Notification Message (PNM)
  - [ ] PNM stored as proof of signature in storage adapter
- [ ] User Interface
  - [ ] HTTP or libp2p access to node
  - [ ] Takes JSON or Flatbuffer input
  - [ ] Converts JSON to flatbuffer, returns flatbuffer if JSON input
  - [ ] Pick messages to store based on producer, message type, and limit
- [ ] Storage adapter
  - [ ] IPFS storage adapter using external IPFS node
  - [ ] Other storage adapters will accept messages using pubsub interface
  - [ ] Implement libp2p [MessageCache](https://github.com/ChainSafe/js-libp2p-gossipsub/blob/f255ae4907ea1eb64272b27534794d6b8be1321d/src/message-cache.ts#L26)
- [ ] Identity management
  - [ ] Use SpaceDataStandards.org EPM
  - [ ] Have 'publish to global' option, sent to IPFS
- [ ] Set up channels per provider
- [ ] Request management
  - [ ] Send a request for service (XXX)
  - [ ] Get a response with new channel name for service (XXX)
  - [ ] Have node (or node provider) sub to channel
  - [ ] Send Publish Notification Message or actual data message to channel

## Marketing

- [ ] Realtime
- [ ] Encrypted / Secure
- [ ] Point to point security (isolated)

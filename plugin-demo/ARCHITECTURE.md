# SDN Architecture — Complete Data Flow Reference

This document provides detailed architecture diagrams for the Space Data Network
data flow, from publication through notification to consumption.

---

## 1. End-to-End Data Flow

```
                          ┌─────────────────────────────────────────────┐
                          │              PUBLISHER NODE                 │
                          │                                             │
  FlatBuffer bytes ──────►│  1. HTTP POST /api/v1/data/publish/{schema} │
  (OMM, CDM, etc.)        │     ├── Validate file_identifier (4 bytes)  │
                          │     ├── Verify auth session (Ed25519)       │
                          │     ├── Check storage quota                 │
                          │     └── Extract index fields                │
                          │                                             │
                          │  2. Store in FlatSQL                        │
                          │     ├── sds_{schema}(cid, data, sig)        │
                          │     └── sdn_record_index(schema, cid, ...)  │
                          │                                             │
                          │  3. Append PLOG entry                       │
                          │     ├── Compute ENTRY_HASH (SHA-256)        │
                          │     ├── Sign ENTRY_HASH (Ed25519)           │
                          │     ├── Chain to PREVIOUS_ENTRY_HASH        │
                          │     └── Store in sdn_log_index              │
                          │                                             │
                          │  4. Broadcast PNM via GossipSub             │
                          │     topic: /spacedatanetwork/sds/PNM.fbs    │
                          │                                             │
                          │  5. Publish PLHD via GossipSub              │
                          │     topic: /spacedatanetwork/sds/PLHD.fbs   │
                          └──────────────┬──────────────────────────────┘
                                         │
                        GossipSub mesh   │
                     (all subscribed     │
                      peers receive)     │
                                         │
                          ┌──────────────▼──────────────────────────────┐
                          │            SUBSCRIBER NODE                  │
                          │                                             │
                          │  6. Receive PNM                             │
                          │     ├── Parse: CID, FILE_ID, peer address   │
                          │     ├── Verify SIGNATURE (Ed25519)          │
                          │     └── Queue in TipQueue                   │
                          │                                             │
                          │  7. Resolve tip/queue config                │
                          │     Priority: source+schema > source >      │
                          │               schema > system default       │
                          │     ├── autoFetch? → fetch by CID           │
                          │     ├── autoPin? → pin with TTL             │
                          │     └── trusted? → skip verification        │
                          │                                             │
                          │  8. Fetch content                           │
                          │     ├── Direct from publisher (multiaddr)   │
                          │     ├── Or from any peer via SDS protocol   │
                          │     └── Verify CID matches content hash     │
                          │                                             │
                          │  9. Store locally                           │
                          │     ├── sds_{schema}(cid, data, sig)        │
                          │     └── sdn_record_index                    │
                          │                                             │
                          │ 10. Fire subscription handlers              │
                          │     onMessage(schema, data, peerId)         │
                          └─────────────────────────────────────────────┘
```

---

## 2. Publication Log Sync Flow

```
    Publisher                                        Subscriber
    ─────────                                        ──────────

    Maintains per-schema log:                        Tracks last_synced per
    PLOG seq=1 → seq=2 → seq=3 → seq=4             (publisher, schema)
         │          │         │         │
         └──hash────┘──hash───┘──hash───┘

    On new publish:                                  On PLHD receive:
    1. Append PLOG(seq=N+1)                          1. Compare HEAD_SEQUENCE
    2. Broadcast PLHD(HEAD=N+1) ────────────────────►   vs. last_synced
       via GossipSub                                    │
                                                        ▼ (if behind)
                                                     2. Open libp2p stream
                                                        protocol: sds-exchange
                                                        │
    3. Handle MsgSyncLog ◄──────────────────────────── 3. Send MsgSyncLog(0x07)
       since_seq, max=500                                  since=last_synced
       │                                                   max_entries=500
       ▼                                                   │
    4. Query sdn_log_index                              ◄──┘
       WHERE sequence > since_seq
       LIMIT max_entries
       │
       ▼
    5. Send MsgSyncReply(0x08) ────────────────────► 6. Receive PLOG entries
       [entry_count][len|data]...                       │
                                                        ▼
                                                     7. Verify hash chain:
                                                        - Recompute ENTRY_HASH
                                                        - Check chain links
                                                        - Verify Ed25519 sigs
                                                        │
                                                        ▼
                                                     8. Fetch missing CIDs
                                                        (data records)
                                                        │
                                                        ▼
                                                     9. Update last_synced
```

---

## 3. Authentication Flow

```
    Browser/Client                              SDN Server
    ──────────────                              ──────────

    HD Wallet:
    mnemonic → seed → SLIP-10
    ├── m/44'/0'/0'/0'/0' → Ed25519 (signing)
    ├── m/44'/0'/0'/1'/0' → X25519 (encryption)
    └── m/44'/0'/0'       → secp256k1 (xpub)

    1. POST /api/auth/challenge ──────────────►
       { xpub, client_pubkey_hex, ts }          2. Generate random challenge
                                                    Store: challenge_id → xpub
       ◄───────────────────────────────────────
       { challenge_id, challenge (base64),
         expires_at }

    3. Sign challenge with Ed25519 key
       signature = Ed25519.sign(
         signingPrivateKey,
         challenge_bytes
       )

    4. POST /api/auth/verify ──────────────────►
       { challenge_id, xpub,                     5. Verify Ed25519 signature
         client_pubkey_hex, challenge,               against xpub-derived pubkey
         signature_hex }                          6. TOFU: bind signing key
                                                     to xpub on first login
       ◄───────────────────────────────────────  7. Set HTTP-only session cookie
       Set-Cookie: sdn_session=...

    8. Subsequent requests:
       Cookie: sdn_session=... ────────────────► Session lookup → authenticated
```

---

## 4. Plugin Lifecycle

```
    SDN Node Startup
    ────────────────

    1. Load config.yaml
       ├── plugin catalog path
       ├── WASM module paths
       └── plugin env vars

    2. Initialize plugin manager
       │
       ▼
    3. For each plugin:
       │
       ├── Load WASM bytes from disk
       │   (may be AES-256-GCM encrypted artifact)
       │
       ├── Create Wazero runtime (32MB limit)
       │   ├── Instantiate WASI imports
       │   └── Register sdn.* host functions
       │
       ├── Instantiate WASM module
       │   ├── Call _initialize (if present)
       │   └── Resolve required exports
       │
       ├── Call plugin_init(identity_seed, 32)
       │   └── Plugin derives its own keys
       │
       ├── Call plugin_get_metadata()
       │   └── Parse JSON → register protocols
       │
       ├── Register HTTP routes
       │   └── /api/v1/plugins/{id}/*
       │
       ├── Register libp2p stream handlers
       │   └── For each protocol in metadata
       │
       └── Start background tasks
           └── DHT announcements, etc.

    Plugin Running
    ──────────────

    HTTP request → httpbridge → malloc → plugin_handle_request → free → response
    libp2p stream → streambridge → malloc → plugin_handle_request → free → reply
```

---

## 5. Network Topology

```
    ┌──────────────────────────────────────────────────────────────────┐
    │                   OPEN INTERNET (Public IPs)                     │
    │                                                                  │
    │   ┌─────────────┐    ┌─────────────┐    ┌─────────────┐        │
    │   │ Full Node A │◄──►│ Full Node B │◄──►│ Full Node C │        │
    │   │ (Go server) │    │ (Go server) │    │ (Go server) │        │
    │   │             │    │             │    │             │        │
    │   │ DHT routing │    │ DHT routing │    │ DHT routing │        │
    │   │ GossipSub   │    │ GossipSub   │    │ GossipSub   │        │
    │   │ FlatSQL DB  │    │ FlatSQL DB  │    │ FlatSQL DB  │        │
    │   │ Plugin Mgr  │    │ Plugin Mgr  │    │ Plugin Mgr  │        │
    │   │ Log Service │    │ Log Service │    │ Log Service │        │
    │   │ PLOG/PLHD   │    │ PLOG/PLHD   │    │ PLOG/PLHD   │        │
    │   └──────┬──────┘    └──────┬──────┘    └──────┬──────┘        │
    │          │                  │                  │                │
    │          │  TCP + WS + QUIC │                  │                │
    │          │                  │                  │                │
    ├──────────┼──────────────────┼──────────────────┼────────────────┤
    │          │                  │                  │                │
    │   ┌──────▼──────┐    ┌─────▼───────┐    ┌─────▼───────┐       │
    │   │ Edge Relay  │    │ Edge Relay  │    │ Edge Relay  │       │
    │   │ Circuit v2  │    │ Circuit v2  │    │ Circuit v2  │       │
    │   └──────┬──────┘    └──────┬──────┘    └──────┬──────┘       │
    │          │                  │                  │                │
    │     NAT / Firewall         │                  │                │
    │          │                  │                  │                │
    │   ┌──────▼──────┐    ┌─────▼───────┐    ┌─────▼───────┐       │
    │   │ Browser     │    │ Desktop App │    │ Node.js     │       │
    │   │ (sdn-js)    │    │ (Electron)  │    │ (sdn-js)    │       │
    │   │             │    │             │    │             │       │
    │   │ Same HD key │    │ Same HD key │    │ Same HD key │       │
    │   │ = Same ID   │    │ = Same ID   │    │ = Same ID   │       │
    │   │             │    │             │    │             │       │
    │   │ IndexedDB   │    │ SQLite      │    │ In-memory   │       │
    │   └─────────────┘    └─────────────┘    └─────────────┘       │
    │              BEHIND NAT / FIREWALL                             │
    └──────────────────────────────────────────────────────────────────┘
```

---

## 6. Encrypted Message Delivery

```
    Sender                                           Recipient
    ──────                                           ─────────

    1. Look up recipient's X25519 public key
       (derived from their xpub via SLIP-10
        at m/44'/0'/account'/1'/0')

    2. Choose encryption mode:

       Mode 1: ECIES (per-message)
       ────────────────────────────
       ephemeral_x25519 = random_keypair()
       shared_secret = X25519(ephemeral_priv, recipient_pub)
       key = HKDF(shared_secret)
       ciphertext = ChaCha20-Poly1305(key, nonce, plaintext)
       envelope = [ephemeral_pub | nonce | ciphertext | tag]

       Mode 2: Session Key
       ─────────────────────
       session_key = AES-256-GCM key (pre-negotiated)
       ciphertext = AES-GCM(session_key, nonce, plaintext)

       Mode 3: Hybrid
       ────────────────
       header = plaintext RoutingHeader
       payload = ECIES(recipient_pub, data)
       message = [header | payload]

    3. Publish encrypted message:
       ├── GossipSub topic: /sdn/data/{listing_id}/{buyer_peer_id}
       └── Or direct libp2p stream to recipient

    Recipient:
    4. Decrypt with X25519 private key
       ├── Extract ephemeral public key
       ├── Compute shared secret
       ├── Derive decryption key (HKDF)
       └── Decrypt ChaCha20-Poly1305

    5. Parse decrypted FlatBuffer
```

---

## 7. GossipSub Topic Map

```
    /spacedatanetwork/sds/PNM.fbs      ← Publish notifications (all schemas)
    /spacedatanetwork/sds/PLOG.fbs     ← Publication log entries
    /spacedatanetwork/sds/PLHD.fbs     ← Publication log head announcements
    /spacedatanetwork/sds/OMM.fbs      ← Orbital Mean-Elements Messages
    /spacedatanetwork/sds/CDM.fbs      ← Conjunction Data Messages
    /spacedatanetwork/sds/EPM.fbs      ← Entity Profile Manifests
    /spacedatanetwork/sds/CAT.fbs      ← Catalog entries
    /spacedatanetwork/sds/TDM.fbs      ← Tracking Data Messages
    /spacedatanetwork/sds/...          ← One topic per schema (44 total)
    /spacedatanetwork/edge-relays      ← Edge relay announcements
    /sdn/storefront/listings           ← Marketplace listings
    /sdn/storefront/purchases          ← Purchase notifications
    /sdn/storefront/reviews            ← Review announcements
    /sdn/data/{listing}/{buyer}        ← Encrypted data delivery channels
```

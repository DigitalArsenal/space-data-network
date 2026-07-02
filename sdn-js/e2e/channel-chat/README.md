# WS9.3 — encrypted channel chat in-browser E2E

Proves the WS9 stack end-to-end in a REAL browser over REAL gossipsub:
two browser members (alice, bob) exchange an encrypted channel message;
an in-page non-member (mallory) and the out-of-page Node hub receive the
same wire bytes and can read nothing.

Flow: alice mints a `ChannelKeys` channel (WS9.1), wraps the content key
one-to-many to the member set (`$ENC`/`$KMF`, RECIPIENT_KEY_ID = member id);
bob recovers the key from HIS envelope; alice publishes a WS9.2 message
envelope (AES-256-GCM under the channel key, header as AAD, ed25519
sender-signed) on `channelChatTopic(id)`; gossipsub carries it via the hub.

## Run

```sh
# 1. hub: websocket rendezvous + gossipsub forwarder + non-member observer
node e2e/channel-chat/hub.mjs           # prints HUB_READY <multiaddr>

# 2. page server: esbuild-bundles entry.mjs, serves with COOP/COEP
node e2e/channel-chat/serve.mjs         # prints SERVE_READY <url>

# 3. open <url>/?hub=<multiaddr> in Chrome; the page logs
#    E2E_RESULT {"ok":true,...} on success. The hub logs
#    HUB_OBSERVED {"bytes":N,"leaksPlaintext":false} for each message.
```

Verified 2026-07-02 via chrome-devtools MCP:
`E2E_RESULT {"ok":true,"memberDecrypted":true,"senderAttributed":true,"outsiderBlocked":true,"wireLeaksPlaintext":false,"epoch":1}`
and hub `HUB_OBSERVED {"bytes":294,"leaksPlaintext":false}`.

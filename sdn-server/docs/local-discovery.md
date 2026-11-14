Local discovery can be disabled explicitly:

```yaml
peers:
  enable_mdns: false
  enable_dht: false
network:
  bootstrap: []
```

`enable_mdns: false` prevents the mDNS service from opening. An explicitly
empty bootstrap list selects no bootstrap peers for either the node or Kubo.
Absent keys preserve the existing defaults. Invalid nonempty bootstrap lists
retain the existing fallback to built-in peers. Save/load cycles preserve the
explicit empty list.

These settings control discovery and bootstrap; existing connections, configured
services, and explicit peer connections have their own configuration.

# `licenseplugin`

`licenseplugin` is the SDN runtime plugin that provides:

- libp2p license stream handling on `/orbpro/license/1.0.0`
- HTTP routes for:
  - `/api/v1/license/*`
  - `/api/v1/plugins/*`

## Package

```go
import "github.com/spacedatanetwork/sdn-server/plugins/licenseplugin"
```

## Registration

```go
pm := plugins.New()
lp := licenseplugin.New()
_ = pm.Register(lp)
_ = pm.StartAll(ctx, plugins.RuntimeContext{
    Host:         h,
    BaseDataPath: "/opt/data",
    PeerID:       h.ID().String(),
    Mode:         "full",
})
```

Then mount HTTP routes:

```go
mux := http.NewServeMux()
pm.RegisterRoutes(mux)
```

## Notes

- In `edge` mode, plugin startup is a no-op.
- `Plugin.Service()` and `Plugin.TokenVerifier()` expose the underlying runtime objects for internal composition.

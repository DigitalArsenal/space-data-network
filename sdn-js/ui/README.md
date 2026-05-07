# SDN UI Development

Local desktop/Kubo:

```sh
SDN_UI_BACKEND=desktop-local \
SDN_UI_API_URL=http://127.0.0.1:5001 \
SDN_UI_GATEWAY_URL=http://127.0.0.1:8081 \
SDN_UI_PROXY_TARGET=http://127.0.0.1:17890 \
npm --prefix sdn-js run dev:sdn-ui
```

Remote SDN:

```sh
SDN_UI_BACKEND=remote-sdn \
SDN_UI_SERVER_URL=https://sdn.spaceaware.io \
npm --prefix sdn-js run dev:sdn-ui
```

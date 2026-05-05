# Storefront Browser E2E

The SDN JS storefront browser harness verifies the marketplace UI purchase
path in Chromium:

```sh
cd sdn-js
npm run test:e2e:storefront
```

The harness serves the marketplace page with Vite and stubs the current
storefront API responses for a paid protected module listing. It proves that
the browser UI can:

- load a paid encrypted storefront listing;
- create a Go-contract purchase payload;
- complete the SDN credits payment path;
- display grant, delivery topic, and encrypted CID state;
- call the browser client-decrypt adapter to fetch encrypted bytes, decrypt,
  and load the resulting fixture module bytes.

The remaining live-daemon gap is fixture availability, not browser wiring. A
fully live check still needs a local SDN daemon/dev hook that seeds all of the
following in one reproducible setup:

- a paid protected storefront listing with `protected_delivery.encrypted_cid`;
- buyer credits or an equivalent dev payment settlement;
- the issued grant's raw REC/KMF bytes or another browser client-decrypt input;
- an encrypted artifact fetch path for the advertised CID.

Until those server fixtures exist, the Playwright harness keeps the browser
purchase-to-grant-to-decrypt/load path reproducible without editing
storefront server or CelesTrak paths.

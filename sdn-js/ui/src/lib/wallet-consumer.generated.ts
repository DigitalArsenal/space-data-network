export const walletConsumer = Object.freeze({
  "clientId": "sdn-node-console-v1",
  "callbackUri": "https://sdn.spaceaware.io/wallet/callback",
  "allowedOperations": [
    "sdn.auth.jcs-envelope.v2",
    "sdn.auth.raw-challenge.v1",
    "sdn.wallet.account.v1",
    "sdn.wallet.connect.v1"
  ],
  "audiences": [
    "sdn-login:sdn.spaceaware.io"
  ],
  "registryReleaseSha256": "e1ce6fe903c9700484a8a87d96581c8cad97063dabf63030b4518a31a3bdaa93",
  "walletVersion": "2.0.28"
} as const);

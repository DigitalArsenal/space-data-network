// Stand-in: sdn-js never configures Let's Encrypt certificates for a browser or Node client (see package.json).
export function autoTLS() {
  throw new Error('autoTLS is not part of sdn-js: it builds its own libp2p node (no ACME, no node-forge)');
}

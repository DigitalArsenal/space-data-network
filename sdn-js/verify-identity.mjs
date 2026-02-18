/**
 * Verify that hd-wallet-wasm JS produces identical results to the Go server
 * for the same mnemonic.
 *
 * Uses hd-wallet-wasm directly, replicating the same derivation paths as the Go server.
 *
 * Key derivation paths (all SLIP-10 hardened):
 *   Identity (secp256k1): m/44'/0'/0'         → xpub, PeerID
 *   Signing  (Ed25519):   m/44'/0'/0'/0'/0'   → auth signatures
 *   Encryption (X25519):  m/44'/0'/0'/1'/0'   → key agreement
 */
import init, { Curve } from 'hd-wallet-wasm';

const MNEMONIC = 'morning radio tomorrow prize wreck nurse try crazy employ library slow cook beyond gorilla recycle antenna welcome inject hedgehog satisfy virus cloth menu glue';

// Expected values from Go server show-identity
const EXPECTED = {
  peerID: '16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45',
  xpub: 'xpub6DKCyLbCHZLFR4XpFg26royZdkxExSMHTjNorEgkn1kgvQbLF5sts9RfNt3PbGhphVUh7WsFQ5H6GJBh4LhmRL27oSPt1qDkJ5mAr6FZ3Wa',
  signingPubHex: '0d80e1fd5f9a4e34dfdf36a0e152bd99a65cfff8bcc6cab2757b484ae442fc8c',
  encryptionPubHex: '08ea56d04396e66d534acd8c973eaf41d3e80edfd39a0712691645fe0b191741',
};

function toHex(buf) {
  return Buffer.from(buf).toString('hex');
}

async function main() {
  const mod = await init();
  const { hdkey, libp2p, curves, slip10 } = mod;

  // 1. Derive seed from mnemonic
  const seed = mod.mnemonic.toSeed(MNEMONIC, '');
  console.log('Seed length:', seed.length, 'bytes');

  // 2. Derive secp256k1 identity key at m/44'/0'/0' (BIP-32)
  const secpMaster = hdkey.fromSeed(seed, Curve.SECP256K1);
  const accountKey = secpMaster.derivePath("m/44'/0'/0'");

  const xpub = accountKey.neutered().toXpub();
  const peerID = accountKey.peerIdString();
  const identityPubKey = accountKey.publicKey(); // 33-byte compressed

  // Also test convenience API: PeerID from xpub string
  const peerIDFromXpub = libp2p.peerIdFromXpub(xpub);

  // 3. Derive Ed25519 signing key at m/44'/0'/0'/0'/0' (SLIP-10)
  const signingResult = slip10.deriveEd25519Path(seed, "m/44'/0'/0'/0'/0'");
  const signingPub = curves.ed25519.publicKeyFromSeed(signingResult.privateKey);

  // 4. Derive X25519 encryption key at m/44'/0'/0'/1'/0' (SLIP-10 → X25519 scalar)
  //    Uses SLIP-10 Ed25519 derivation, then interprets the 32-byte key as X25519 scalar
  const encResult = slip10.deriveEd25519Path(seed, "m/44'/0'/0'/1'/0'");
  const encPub = curves.x25519.publicKey(encResult.privateKey);

  // Clean up
  secpMaster.wipe();
  accountKey.wipe();

  console.log('\n--- hd-wallet-wasm JS Results ---');
  console.log('PeerID:              ', peerID);
  console.log('PeerID (from xpub):  ', peerIDFromXpub);
  console.log('XPub:                ', xpub);
  console.log('Identity Key (pub):  ', toHex(identityPubKey), `(${identityPubKey.length} bytes, secp256k1)`);
  console.log('Signing Key (pub):   ', toHex(signingPub), `(${signingPub.length} bytes, Ed25519)`);
  console.log('Encryption Key (pub):', toHex(encPub), `(${encPub.length} bytes, X25519)`);

  console.log('\n--- Comparison with Go Server ---');
  let allMatch = true;

  const checks = [
    ['PeerID', peerID, EXPECTED.peerID],
    ['PeerID (from xpub)', peerIDFromXpub, EXPECTED.peerID],
    ['XPub', xpub, EXPECTED.xpub],
    ['Signing Key (hex)', toHex(signingPub), EXPECTED.signingPubHex],
    ['Encryption Key (hex)', toHex(encPub), EXPECTED.encryptionPubHex],
  ];

  for (const [name, actual, expected] of checks) {
    const match = actual === expected;
    if (!match) allMatch = false;
    console.log(`${match ? 'PASS' : 'FAIL'} ${name}: ${match ? 'matches' : `\n  Go:   ${expected}\n  JS:   ${actual}`}`);
  }

  console.log(`\n${allMatch ? 'ALL CHECKS PASSED — Go server and hd-wallet-wasm JS produce identical results' : 'SOME CHECKS FAILED'}`);
  process.exit(allMatch ? 0 : 1);
}

main().catch(err => {
  console.error(err);
  process.exit(1);
});

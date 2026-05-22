import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const root = path.dirname(fileURLToPath(import.meta.url));
const target = path.resolve(root, '../node_modules/hd-wallet-ui/src/app.js');

if (!fs.existsSync(target)) {
  console.warn('[patch-hd-wallet-ui] hd-wallet-ui source not found; skipping patch');
  process.exit(0);
}

const source = fs.readFileSync(target, 'utf8');
if (source.includes('const sdnSigning = getSigningKey(state.hdRoot, 0, sdnAccount, sdnIndex);')) {
  process.exit(0);
}

const oldBlocks = [`      const sdnSigning = getSigningKey(state.hdRoot, 0, 0, 0);
      const sdnPrivKey = sdnSigning.privateKey;
      const sdnPubKey = hdWallet().curves.ed25519.publicKeyFromSeed(sdnPrivKey);
      // Don't keep derived private key bytes around longer than needed.
      if (sdnPrivKey instanceof Uint8Array) sdnPrivKey.fill(0);
      const xpub = state.hdRoot.toXpub();
      _onLoginCallback({
        xpub,
        peerId: deriveAccountPeerId(),
        signingPublicKey: sdnPubKey,
        async sign(message) {
          const msgBytes = typeof message === 'string'
            ? new TextEncoder().encode(message)
            : message;
          const signing = getSigningKey(state.hdRoot, 0, 0, 0);
          try {
            return hdWallet().curves.ed25519.sign(msgBytes, signing.privateKey);
          } finally {
            if (signing?.privateKey instanceof Uint8Array) signing.privateKey.fill(0);
          }
        },
      });`, `      const sdnAccount = 0;
      const sdnIndex = 0;
      const sdnAccountXpubKey = state.hdRoot.derivePath(\`m/44'/0'/\${sdnAccount}'\`);
      const xpub = sdnAccountXpubKey.toXpub();
      const identityPublicKey = sdnAccountXpubKey.publicKey();
      sdnAccountXpubKey.wipe?.();

      const sdnSigning = state.hdRoot.derivePath(\`m/44'/0'/\${sdnAccount}'/0'/\${sdnIndex}'\`);
      const sdnPrivKey = sdnSigning.privateKey();
      const sdnPubKey = hdWallet().curves.ed25519.publicKeyFromSeed(sdnPrivKey);
      sdnSigning.wipe?.();

      const sdnEncryption = state.hdRoot.derivePath(\`m/44'/0'/\${sdnAccount}'/1'/\${sdnIndex}'\`);
      const sdnEncryptionPrivKey = sdnEncryption.privateKey();
      const sdnEncryptionPubKey = hdWallet().curves.x25519.publicKey(sdnEncryptionPrivKey);
      sdnEncryption.wipe?.();

      // Don't keep derived private key bytes around longer than needed.
      if (sdnPrivKey instanceof Uint8Array) sdnPrivKey.fill(0);
      if (sdnEncryptionPrivKey instanceof Uint8Array) sdnEncryptionPrivKey.fill(0);
      _onLoginCallback({
        xpub,
        peerId: deriveAccountPeerId(),
        walletAccountId: String(sdnAccount),
        walletAccountLabel: \`Account \${sdnAccount}\`,
        identityPublicKey,
        signingPublicKey: sdnPubKey,
        encryptionPublicKey: sdnEncryptionPubKey,
        async sign(message) {
          const msgBytes = typeof message === 'string'
            ? new TextEncoder().encode(message)
            : message;
          const signing = state.hdRoot.derivePath(\`m/44'/0'/\${sdnAccount}'/0'/\${sdnIndex}'\`);
          const privateKey = signing.privateKey();
          try {
            return hdWallet().curves.ed25519.sign(msgBytes, privateKey);
          } finally {
            if (privateKey instanceof Uint8Array) privateKey.fill(0);
            signing.wipe?.();
          }
        },
      });`];

const newBlock = `      const sdnAccount = 0;
      const sdnIndex = 0;
      const sdnAccountXpubKey = state.hdRoot.derivePath(\`m/44'/0'/\${sdnAccount}'\`);
      const xpub = sdnAccountXpubKey.toXpub();
      const identityPublicKey = sdnAccountXpubKey.publicKey();
      sdnAccountXpubKey.wipe?.();

      const sdnSigning = getSigningKey(state.hdRoot, 0, sdnAccount, sdnIndex);
      const sdnEncryption = getEncryptionKey(state.hdRoot, 0, sdnAccount, sdnIndex);
      const sdnPrivKey = sdnSigning.privateKey;
      const sdnEncryptionPrivKey = sdnEncryption.privateKey;
      _onLoginCallback({
        xpub,
        peerId: deriveAccountPeerId(),
        walletAccountId: String(sdnAccount),
        walletAccountLabel: \`Account \${sdnAccount}\`,
        identityPublicKey,
        signingPublicKey: sdnSigning.publicKey,
        encryptionPublicKey: sdnEncryption.publicKey,
        async sign(message) {
          const msgBytes = typeof message === 'string'
            ? new TextEncoder().encode(message)
            : message;
          const signing = getSigningKey(state.hdRoot, 0, sdnAccount, sdnIndex);
          try {
            const digest = await crypto.subtle.digest('SHA-256', msgBytes);
            return hdWallet().curves.secp256k1.sign(new Uint8Array(digest), signing.privateKey);
          } finally {
            if (signing?.privateKey instanceof Uint8Array) signing.privateKey.fill(0);
          }
        },
      });`;

const oldBlock = oldBlocks.find((block) => source.includes(block));
if (!oldBlock) {
  throw new Error('[patch-hd-wallet-ui] expected login callback block was not found');
}

let next = source.replace(oldBlock, newBlock);
next = next.replace('      });\n    } catch (err) {', `      });
      if (sdnPrivKey instanceof Uint8Array) sdnPrivKey.fill(0);
      if (sdnEncryptionPrivKey instanceof Uint8Array) sdnEncryptionPrivKey.fill(0);
    } catch (err) {`);
fs.writeFileSync(target, next);
console.log('[patch-hd-wallet-ui] patched hd-wallet-ui SDN login payload');

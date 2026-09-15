export const HD_WALLET_SDN_PLUGIN_ID =
  'com.digitalarsenal.infrastructure.hd-wallet-wasm';

export const HD_WALLET_SDN_PLUGIN_MANIFEST = Object.freeze({
  pluginId: HD_WALLET_SDN_PLUGIN_ID,
  name: 'HD Wallet Crypto',
  version: '2.0.1',
  pluginFamily: 'infrastructure',
  description:
    'Native-crypto infrastructure plugin surface for detached signing and field-level encryption inside an sdn-flow runtime.',
  capabilities: [
    {
      capabilityId: 'random',
      required: true,
      description:
        'Explicit host entropy for IVs, salts, and ephemeral encryption material.',
    },
    {
      capabilityId: 'wallet_sign',
      required: false,
      description:
        'Optional host-resident key surface for signing when private key bytes are not carried in the request payload.',
    },
  ],
  externalInterfaces: [
    {
      interfaceId: 'wallet-active-key',
      kind: 'host-service',
      direction: 'bidirectional',
      capability: 'wallet_sign',
      resource: 'wallet://active-key',
      required: false,
      description:
        'Optional host-provided signing and key-agreement surface for resident key material.',
      properties: {
        curves: ['secp256k1', 'ed25519', 'x25519'],
        residentKeyMaterial: true,
      },
    },
    {
      interfaceId: 'host-rng',
      kind: 'host-service',
      direction: 'input',
      capability: 'random',
      resource: 'host-rng://default',
      required: true,
      description:
        'Explicit entropy source injected by the host for salts, IVs, and ephemeral sender keys.',
      properties: {
        provides: ['salt', 'iv', 'ephemeral-private-key'],
      },
    },
  ],
  methods: [
    {
      methodId: 'encrypt_fields',
      displayName: 'Encrypt Fields',
      inputPorts: [
        {
          portId: 'field_set',
          acceptedTypeSets: [
            {
              setId: 'field-selection-bundle',
              allowedTypes: [
                {
                  schemaName: 'FieldSelectionBundle.fbs',
                  fileIdentifier: 'FSLB',
                },
              ],
              description:
                'Selected plaintext fields plus key agreement material.',
            },
          ],
          minStreams: 1,
          maxStreams: 1,
          required: true,
          description:
            'Field bundle containing plaintext values to encrypt and recipient key material.',
        },
      ],
      outputPorts: [
        {
          portId: 'encrypted_fields',
          acceptedTypeSets: [
            {
              setId: 'encrypted-field-set',
              allowedTypes: [
                {
                  schemaName: 'EncryptedFieldSet.fbs',
                  fileIdentifier: 'EFLD',
                },
              ],
              description:
                'Encrypted field envelopes emitted by hd-wallet-wasm.',
            },
          ],
          minStreams: 1,
          maxStreams: 1,
          required: true,
          description: 'Encrypted field envelopes with salts, IVs, and tags.',
        },
      ],
      maxBatch: 64,
      drainPolicy: 'drain-until-yield',
      description:
        'Encrypts selected field payloads without leaving the plugin runtime contract.',
    },
    {
      methodId: 'decrypt_fields',
      displayName: 'Decrypt Fields',
      inputPorts: [
        {
          portId: 'encrypted_fields',
          acceptedTypeSets: [
            {
              setId: 'encrypted-field-set',
              allowedTypes: [
                {
                  schemaName: 'EncryptedFieldSet.fbs',
                  fileIdentifier: 'EFLD',
                },
              ],
              description:
                'Encrypted field envelopes emitted by encrypt_fields.',
            },
          ],
          minStreams: 1,
          maxStreams: 1,
          required: true,
          description:
            'Encrypted field envelopes plus recipient private key material.',
        },
      ],
      outputPorts: [
        {
          portId: 'field_set',
          acceptedTypeSets: [
            {
              setId: 'field-selection-bundle',
              allowedTypes: [
                {
                  schemaName: 'FieldSelectionBundle.fbs',
                  fileIdentifier: 'FSLB',
                },
              ],
              description:
                'Recovered plaintext field bundle after authenticated decryption.',
            },
          ],
          minStreams: 1,
          maxStreams: 1,
          required: true,
          description: 'Recovered plaintext fields.',
        },
      ],
      maxBatch: 64,
      drainPolicy: 'drain-until-yield',
      description:
        'Performs authenticated field decryption using the manifest-defined plugin surface.',
    },
    {
      methodId: 'sign_detached',
      displayName: 'Sign Detached',
      inputPorts: [
        {
          portId: 'message',
          acceptedTypeSets: [
            {
              setId: 'detached-signing-request',
              allowedTypes: [
                {
                  schemaName: 'DetachedSigningRequest.fbs',
                  fileIdentifier: 'SGRQ',
                },
              ],
              description:
                'Message payload and signing material for detached signatures.',
            },
          ],
          minStreams: 1,
          maxStreams: 1,
          required: true,
          description: 'Signing request payload.',
        },
      ],
      outputPorts: [
        {
          portId: 'signature',
          acceptedTypeSets: [
            {
              setId: 'detached-signature',
              allowedTypes: [
                {
                  schemaName: 'DetachedSignature.fbs',
                  fileIdentifier: 'SIGD',
                },
              ],
              description: 'Detached signature envelope.',
            },
          ],
          minStreams: 1,
          maxStreams: 1,
          required: true,
          description: 'Detached signature plus digest and public key metadata.',
        },
      ],
      maxBatch: 64,
      drainPolicy: 'drain-until-yield',
      description:
        'Signs payloads through hd-wallet-wasm primitives instead of ad hoc host helpers.',
    },
    {
      methodId: 'verify_detached',
      displayName: 'Verify Detached',
      inputPorts: [
        {
          portId: 'signature',
          acceptedTypeSets: [
            {
              setId: 'detached-signature',
              allowedTypes: [
                {
                  schemaName: 'DetachedSignature.fbs',
                  fileIdentifier: 'SIGD',
                },
              ],
              description:
                'Detached signature envelope emitted by sign_detached.',
            },
          ],
          minStreams: 1,
          maxStreams: 1,
          required: true,
          description: 'Detached signature envelope to verify.',
        },
      ],
      outputPorts: [
        {
          portId: 'verification',
          acceptedTypeSets: [
            {
              setId: 'detached-verification-result',
              allowedTypes: [
                {
                  schemaName: 'DetachedVerificationResult.fbs',
                  fileIdentifier: 'SIGV',
                },
              ],
              description: 'Detached signature verification result.',
            },
          ],
          minStreams: 1,
          maxStreams: 1,
          required: true,
          description: 'Verification outcome and normalized signature metadata.',
        },
      ],
      maxBatch: 64,
      drainPolicy: 'drain-until-yield',
      description:
        'Verifies detached signatures through the plugin contract using WASM-backed crypto only.',
    },
  ],
  schemasUsed: [
    {
      schemaName: 'FieldSelectionBundle.fbs',
      fileIdentifier: 'FSLB',
    },
    {
      schemaName: 'EncryptedFieldSet.fbs',
      fileIdentifier: 'EFLD',
    },
    {
      schemaName: 'DetachedSigningRequest.fbs',
      fileIdentifier: 'SGRQ',
    },
    {
      schemaName: 'DetachedSignature.fbs',
      fileIdentifier: 'SIGD',
    },
    {
      schemaName: 'DetachedVerificationResult.fbs',
      fileIdentifier: 'SIGV',
    },
  ],
  buildArtifacts: [
    {
      artifactId: 'hd-wallet-wasm-browser',
      kind: 'wasm-module',
      path: 'wasm/dist/hd-wallet.wasm',
      target: 'browser',
      entrySymbol: 'plugin_get_manifest_flatbuffer',
    },
    {
      artifactId: 'hd-wallet-wasm-wasi',
      kind: 'wasm-module',
      path: 'wasm/dist/hd-wallet-wasi.wasm',
      target: 'wasi',
      entrySymbol: 'plugin_get_manifest_flatbuffer',
    },
    {
      artifactId: 'hd-wallet-wasm-loader',
      kind: 'javascript-loader',
      path: 'wasm/dist/hd-wallet.js',
      target: 'browser',
      entrySymbol: 'HDWalletWasm',
    },
  ],
  abiVersion: 1,
});

export function cloneSdnPluginManifest() {
  return JSON.parse(JSON.stringify(HD_WALLET_SDN_PLUGIN_MANIFEST));
}

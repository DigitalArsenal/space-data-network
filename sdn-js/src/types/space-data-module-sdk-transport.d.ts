declare module 'space-data-module-sdk/transport' {
  export interface ExtractedPublicationRecordCollectionLike {
    enc?: unknown;
  }

  export interface DecryptProtectedBytesOptions {
    protectedBytes: Uint8Array;
    recipientPrivateKey: Uint8Array;
  }

  export interface EncryptBytesForRecipientOptions {
    plaintext: Uint8Array;
    recipientPublicKey: Uint8Array;
    context?: string;
    rootType?: string | null;
  }

  export interface EncryptedEnvelopePayloadLike {
    protectedBlobBase64: string;
  }

  export function decryptProtectedBytes(
    options: DecryptProtectedBytesOptions,
  ): Promise<Uint8Array>;

  export function extractPublicationRecordCollection(
    protectedBytes: Uint8Array,
  ): ExtractedPublicationRecordCollectionLike | null;

  export function encryptBytesForRecipient(
    options: EncryptBytesForRecipientOptions,
  ): Promise<EncryptedEnvelopePayloadLike>;

  export function generateX25519Keypair(): Promise<{
    publicKey: Uint8Array;
    privateKey: Uint8Array;
  }>;
}

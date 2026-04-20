export interface ProviderDescriptor {
  publicKey: string;
  peerId: string;
  relayAddresses: string[];
  ipns?: string;
  identity?: {
    xpub?: string;
    identityPublicKey?: string;
    signingPublicKey?: string;
    encryptionPublicKey?: string;
    ipnsEntries?: string[];
    ensNames?: string[];
    addresses?: Array<{
      chain: string;
      address: string;
      keyPath?: string;
      publicKey?: string;
    }>;
  };
}

export interface ModuleDeliveryEventLike {
  stage: string;
  detail?: string;
  cid?: string;
  providerPeerId?: string;
}

export interface RuntimeIdentityLike {
  encryptionKey: {
    privateKey: Uint8Array;
  };
}

export interface RuntimeNodeLike {
  dial: (address: string) => Promise<void>;
  requestModuleGrant: (options: Record<string, unknown>) => Promise<unknown>;
  discoverProviders: (
    discoveryCID: string,
  ) => Promise<Array<{ peerId: string; multiaddrs: string[] }>>;
}

export interface RuntimeModules {
  initHDWallet: () => Promise<boolean>;
  deriveIdentity: (entropy: Uint8Array) => Promise<RuntimeIdentityLike>;
  randomBytes: (length: number) => Uint8Array;
  discoverProvider: (publicKey: Uint8Array) => Promise<{ discoveryCID: string }>;
  fetchEncryptedModuleBundle: (
    node: RuntimeNodeLike,
    grant: unknown,
    options?: {
      onEvent?: (event: ModuleDeliveryEventLike) => void;
    },
  ) => Promise<{
    grant: {
      wrappedContentKey: unknown;
      bundleDescriptor: {
        cid: string;
      };
    };
    encryptedBundleBytes: Uint8Array;
  }>;
  SDNNode: {
    create: (
      config: Record<string, unknown>,
      handlers: {
        onPeerConnected?: (peerId: string) => void;
        onPeerDisconnected?: (peerId: string) => void;
        onModuleDeliveryEvent?: (event: ModuleDeliveryEventLike) => void;
      },
    ) => Promise<RuntimeNodeLike>;
  };
  normalizeAddressLookupKey: (chain: string, value: string) => Promise<{
    normalizedValue: string;
    discoveryCID: string;
  }>;
  decryptEncryptedModuleBundle: (
    encryptedBundleBytes: Uint8Array,
    contentKey: Uint8Array,
    observer?: {
      onEvent?: (event: ModuleDeliveryEventLike) => void;
    },
  ) => Promise<Uint8Array>;
  invokeLoadedModule: <TResult = unknown>(
    harness: unknown,
    request: unknown,
    observer?: {
      onEvent?: (event: ModuleDeliveryEventLike) => void;
    },
  ) => Promise<TResult>;
  loadDecryptedModule: (
    wasmBytes: Uint8Array,
    observer?: {
      onEvent?: (event: ModuleDeliveryEventLike) => void;
    },
  ) => Promise<unknown>;
  unwrapGrantContentKey: (
    wrappedContentKey: unknown,
    recipientPrivateKey: Uint8Array,
    observer?: {
      onEvent?: (event: ModuleDeliveryEventLike) => void;
    },
  ) => Promise<Uint8Array>;
}

export interface DirectoryUserLike {
  xpub: string;
  name?: string;
  trust_level?: string;
  source?: string;
  last_login?: string;
}

export type StoreSelection =
  | { kind: 'author'; key: string }
  | { kind: 'plugin'; key: string }
  | { kind: 'data'; key: string };

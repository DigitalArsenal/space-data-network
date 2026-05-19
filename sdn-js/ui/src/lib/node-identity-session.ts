import type {
  BackendResult,
  NodeIdentityApplyResult,
  NodeIdentitySettings,
  SdnBackend,
  WalletNodeIdentityPayload,
} from '../../../src/ui/runtime/sdn-backend';
import type { MountedWalletUI, WalletLoginPayload, WalletUIOptions } from '../../../src/ui/runtime/wallet-ui';

const DEFAULT_UNLOCK_TTL_MS = 60 * 60 * 1000;
type MountWalletUI = (host: HTMLElement, options?: WalletUIOptions) => Promise<MountedWalletUI>;

export interface NodeIdentitySessionState {
  locked: boolean;
  settings: NodeIdentitySettings;
  sessionExpiresAt: number | null;
  status: string;
  mismatch: NodeIdentityApplyResult | null;
  profile: Record<string, unknown> | null;
}

export interface NodeIdentitySessionController {
  readonly state: NodeIdentitySessionState;
  loadSettings(): Promise<void>;
  mountWallet(host: HTMLElement): Promise<MountedWalletUI | null>;
  openLogin(): Promise<void>;
  confirmNodeIdentityReplacement(): Promise<void>;
  declineNodeIdentityReplacement(): void;
  saveSettings(settings: NodeIdentitySettings): Promise<BackendResult<NodeIdentitySettings> | null>;
  logout(): Promise<void>;
  destroy(): void;
}

export interface NodeIdentitySessionOptions {
  backend: SdnBackend;
  onStateChange?: (state: NodeIdentitySessionState) => void;
  mountWallet?: MountWalletUI;
  now?: () => number;
}

export function createNodeIdentitySessionController(options: NodeIdentitySessionOptions): NodeIdentitySessionController {
  const now = options.now ?? (() => Date.now());
  const walletMount = options.mountWallet ?? loadWalletUI;
  let mountedWallet: MountedWalletUI | null = null;
  let mountedHost: HTMLElement | null = null;
  let expirationTimer: ReturnType<typeof setTimeout> | null = null;
  let pendingPayload: WalletNodeIdentityPayload | null = null;

  const state: NodeIdentitySessionState = {
    locked: true,
    settings: { ttlMs: DEFAULT_UNLOCK_TTL_MS },
    sessionExpiresAt: null,
    status: 'Locked',
    mismatch: null,
    profile: null,
  };

  function publish(patch: Partial<NodeIdentitySessionState> = {}): void {
    Object.assign(state, patch);
    options.onStateChange?.({ ...state });
  }

  async function loadSettings(): Promise<void> {
    const result = await options.backend.getNodeIdentitySettings();
    if (result.ok && result.data) {
      if (restorePersistedSession(result.data)) return;
      publish({ settings: result.data });
      return;
    }
    publish({ status: result.capability.reason ?? 'Identity settings unavailable.' });
  }

  async function mountWallet(host: HTMLElement): Promise<MountedWalletUI | null> {
    if (!host) return null;
    if (mountedWallet && mountedHost === host) return mountedWallet;
    await mountedWallet?.destroy?.();
    mountedHost = host;
    mountedWallet = await walletMount(host, {
      backend: options.backend,
      backendMode: options.backend.mode,
      onLogin: handleWalletLogin,
      openAccountAfterLogin: false,
    });
    return mountedWallet;
  }

  async function openLogin(): Promise<void> {
    if (!mountedWallet && mountedHost) {
      await mountWallet(mountedHost);
    }
    if (!mountedWallet?.openLogin) {
      publish({ status: 'Wallet login is not ready yet.' });
      return;
    }
    await mountedWallet.openLogin();
  }

  async function handleWalletLogin(login: WalletLoginPayload): Promise<void> {
    publish({ status: 'Signing node identity...', mismatch: null });
    const payload = await walletNodeIdentityPayload(login);
    pendingPayload = payload;
    await applyWalletPayload(payload, false);
  }

  async function applyWalletPayload(payload: WalletNodeIdentityPayload, replace: boolean): Promise<void> {
    const result = await options.backend.applyWalletNodeIdentity(payload, { replace });
    if (!result.ok || !result.data) {
      publish({ status: result.capability.reason ?? 'Node identity update failed.' });
      return;
    }
    if (result.data.status === 'mismatch') {
      publish({
        locked: true,
        sessionExpiresAt: null,
        status: 'Wallet key change needs confirmation.',
        mismatch: result.data,
      });
      return;
    }
    pendingPayload = null;
    beginUnlockedSession(result.data.profile ?? null);
  }

  async function confirmNodeIdentityReplacement(): Promise<void> {
    if (!pendingPayload) {
      publish({ status: 'No pending wallet identity replacement.' });
      return;
    }
    publish({ status: 'Replacing node identity...', mismatch: null });
    await applyWalletPayload(pendingPayload, true);
  }

  function declineNodeIdentityReplacement(): void {
    pendingPayload = null;
    publish({ status: 'Locked', mismatch: null });
  }

  async function saveSettings(settings: NodeIdentitySettings): Promise<BackendResult<NodeIdentitySettings> | null> {
    const result = await options.backend.saveNodeIdentitySettings(settings);
    if (result.ok && result.data) {
      if (!state.locked && restorePersistedSession(result.data)) return result;
      publish({ settings: result.data, status: state.locked ? state.status : 'Unlocked' });
      if (!state.locked) beginUnlockedSession(state.profile);
    } else {
      publish({ status: result.capability.reason ?? 'Unable to save unlock duration.' });
    }
    return result;
  }

  async function logout(): Promise<void> {
    clearExpirationTimer();
    pendingPayload = null;
    await options.backend.logoutNodeIdentity();
    publish({
      locked: true,
      sessionExpiresAt: null,
      status: 'Locked',
      mismatch: null,
      profile: null,
    });
  }

  function beginUnlockedSession(profile: Record<string, unknown> | null): void {
    clearExpirationTimer();
    const ttl = state.settings.ttlMs;
    const sessionExpiresAt = ttl === 'app' ? null : now() + Math.max(1, ttl);
    scheduleExpiration(sessionExpiresAt);
    publish({
      locked: false,
      sessionExpiresAt,
      status: 'Unlocked',
      mismatch: null,
      profile,
    });
  }

  function restorePersistedSession(settings: NodeIdentitySettings): boolean {
    const session = settings.session;
    if (!session?.unlocked) return false;
    const sessionExpiresAt = session.expiresAt ? Date.parse(session.expiresAt) : null;
    if (sessionExpiresAt !== null && (!Number.isFinite(sessionExpiresAt) || sessionExpiresAt <= now())) return false;
    clearExpirationTimer();
    scheduleExpiration(sessionExpiresAt);
    publish({
      locked: false,
      settings,
      sessionExpiresAt,
      status: 'Unlocked',
      mismatch: null,
      profile: session.profile ?? null,
    });
    return true;
  }

  function scheduleExpiration(sessionExpiresAt: number | null): void {
    if (sessionExpiresAt === null) return;
    expirationTimer = setTimeout(() => {
      void logout();
    }, Math.max(1, sessionExpiresAt - now()));
  }

  function clearExpirationTimer(): void {
    if (expirationTimer) {
      clearTimeout(expirationTimer);
      expirationTimer = null;
    }
  }

  function destroy(): void {
    clearExpirationTimer();
    void mountedWallet?.destroy?.();
    mountedWallet = null;
    mountedHost = null;
    pendingPayload = null;
  }

  publish();

  return {
    state,
    loadSettings,
    mountWallet,
    openLogin,
    confirmNodeIdentityReplacement,
    declineNodeIdentityReplacement,
    saveSettings,
    logout,
    destroy,
  };
}

async function walletNodeIdentityPayload(login: WalletLoginPayload): Promise<WalletNodeIdentityPayload> {
  const signingPublicKey = bytesToHex(login.signingPublicKey ?? login.publicKey);
  const peerId = stringValue(login.peerId);
  if (!peerId) throw new Error('hd-wallet-ui login did not provide a peer ID.');
  if (!signingPublicKey) throw new Error('hd-wallet-ui login did not provide a signing public key.');

  const signatureTimestamp = Math.floor(Date.now() / 1000);
  const identity: WalletNodeIdentityPayload = {
    peerId,
    xpub: stringValue(login.xpub),
    walletAccountId: stringValue(login.walletAccountId),
    walletAccountLabel: stringValue(login.walletAccountLabel),
    identityPublicKey: bytesToHex(login.identityPublicKey),
    signingPublicKey,
    encryptionPublicKey: bytesToHex(login.encryptionPublicKey),
    signatureTimestamp,
  };
  identity.signaturePayload = canonicalIdentityPayload(identity);
  if (login.sign) {
    const signature = await login.sign(new TextEncoder().encode(identity.signaturePayload));
    identity.signature = bytesToHex(signature);
  }
  return identity;
}

function canonicalIdentityPayload(identity: WalletNodeIdentityPayload): string {
  return JSON.stringify({
    encryption_public_key: identity.encryptionPublicKey ?? '',
    identity_public_key: identity.identityPublicKey ?? '',
    peer_id: identity.peerId,
    signature_timestamp: identity.signatureTimestamp ?? 0,
    signing_public_key: identity.signingPublicKey,
    wallet_account_id: identity.walletAccountId ?? '',
    wallet_account_label: identity.walletAccountLabel ?? '',
    xpub: identity.xpub ?? '',
  });
}

export function bytesToHex(value: unknown): string {
  if (!value) return '';
  if (typeof value === 'string') return value.trim();
  if (value instanceof Uint8Array) {
    return Array.from(value, (byte) => byte.toString(16).padStart(2, '0')).join('');
  }
  if (value instanceof ArrayBuffer) {
    return bytesToHex(new Uint8Array(value));
  }
  if (Array.isArray(value) && value.every((entry) => Number.isInteger(entry))) {
    return bytesToHex(new Uint8Array(value as number[]));
  }
  return '';
}

function stringValue(value: unknown): string | undefined {
  if (typeof value === 'number') return String(value);
  return typeof value === 'string' && value.trim() ? value.trim() : undefined;
}

async function loadWalletUI(host: HTMLElement, options?: WalletUIOptions): Promise<MountedWalletUI> {
  const runtime = await import('../../../src/ui/runtime/wallet-ui');
  return runtime.mountWalletUI(host, options);
}

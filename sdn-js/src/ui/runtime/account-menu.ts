import type { AdminSnapshot } from './admin-adapter';
import type { MountedWalletUI } from './wallet-ui';

export interface AccountMenuControllerOptions {
  mountWalletUI?: () => Promise<MountedWalletUI | void>;
  onSignOut?: () => Promise<void>;
}

export interface AccountMenuSnapshot {
  isOpen: boolean;
  canSignOut: boolean;
  mode: AdminSnapshot['mode'];
  role: AdminSnapshot['permissions']['role'];
  title: string;
  subtitle: string;
}

export interface AccountMenuController {
  open(): Promise<AccountMenuSnapshot>;
  close(): AccountMenuSnapshot;
  toggle(): Promise<AccountMenuSnapshot>;
  openWalletAccount(): Promise<void>;
  signOut(): Promise<void>;
  setAdminSnapshot(snapshot: AdminSnapshot): AccountMenuSnapshot;
  snapshot(): AccountMenuSnapshot;
}

export function createAccountMenuController(
  options: AccountMenuControllerOptions = {},
): AccountMenuController {
  let currentAdminSnapshot = createDefaultAdminSnapshot();
  let isOpen = false;
  let mountedWalletPromise: Promise<MountedWalletUI | void> | null = null;

  return {
    async open(): Promise<AccountMenuSnapshot> {
      isOpen = true;
      await ensureWallet();
      return buildSnapshot();
    },

    close(): AccountMenuSnapshot {
      isOpen = false;
      return buildSnapshot();
    },

    async toggle(): Promise<AccountMenuSnapshot> {
      return isOpen ? this.close() : this.open();
    },

    async openWalletAccount(): Promise<void> {
      const wallet = await ensureWallet();
      await wallet?.openAccount?.();
    },

    async signOut(): Promise<void> {
      if (!canSignOut(currentAdminSnapshot)) {
        return;
      }
      await options.onSignOut?.();
      isOpen = false;
    },

    setAdminSnapshot(snapshot: AdminSnapshot): AccountMenuSnapshot {
      currentAdminSnapshot = snapshot;
      return buildSnapshot();
    },

    snapshot(): AccountMenuSnapshot {
      return buildSnapshot();
    },
  };

  function buildSnapshot(): AccountMenuSnapshot {
    return {
      isOpen,
      canSignOut: canSignOut(currentAdminSnapshot),
      mode: currentAdminSnapshot.mode,
      role: currentAdminSnapshot.permissions.role,
      title: currentAdminSnapshot.nodeContext.displayName,
      subtitle: currentAdminSnapshot.serverTarget?.baseUrl
        ?? currentAdminSnapshot.nodeContext.xpub
        ?? 'Browser-local backend',
    };
  }

  async function ensureWallet(): Promise<MountedWalletUI | void> {
    if (!options.mountWalletUI) {
      return undefined;
    }
    if (!mountedWalletPromise) {
      mountedWalletPromise = options.mountWalletUI();
    }
    return mountedWalletPromise;
  }
}

function canSignOut(snapshot: AdminSnapshot): boolean {
  return snapshot.mode === 'server' && snapshot.permissions.authenticated;
}

function createDefaultAdminSnapshot(): AdminSnapshot {
  return {
    mode: 'local',
    serverTarget: null,
    nodeContext: {
      displayName: 'Local backend',
      peerId: null,
      xpub: null,
      transport: 'helia',
      descriptorUrl: null,
    },
    permissions: {
      authenticated: false,
      role: 'guest',
      canManageUsers: false,
      canManageFrontend: false,
      canManageStore: false,
      canOpenWallet: true,
    },
    workspace: {
      activeId: 'network',
      available: ['network', 'directory', 'store', 'frontend', 'wallet'],
    },
  };
}

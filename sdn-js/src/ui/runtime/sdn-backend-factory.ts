import { normalizeBackendConfig, type PartialSdnBackendConfig, type SdnBackend } from './sdn-backend';
import { createBrowserNodeBackend } from './sdn-backend-browser';
import { createDesktopLocalBackend } from './sdn-backend-desktop';
import { createRemoteSdnBackend } from './sdn-backend-remote';

export function createSdnBackend(config: PartialSdnBackendConfig): SdnBackend {
  const normalized = normalizeBackendConfig(config);
  if (normalized.mode === 'remote-sdn') return createRemoteSdnBackend(normalized);
  if (normalized.mode === 'browser-node') return createBrowserNodeBackend();
  return createDesktopLocalBackend(normalized);
}

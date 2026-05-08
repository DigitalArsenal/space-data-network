import type { SdnBackend } from '../../../src/ui/runtime/sdn-backend';
import { createSdnBackend } from '../../../src/ui/runtime/sdn-backend-factory';
import { readHostedServerBaseUrl, type HostedRuntimeConfigWindow } from '../../../src/ui/runtime/runtime-config';

type SdnUiEnv = ImportMetaEnv & {
  readonly SDN_UI_BACKEND?: string;
  readonly SDN_UI_API_URL?: string;
  readonly SDN_UI_GATEWAY_URL?: string;
  readonly SDN_UI_PROXY_TARGET?: string;
  readonly SDN_UI_SERVER_URL?: string;
};

export function createBackendFromLocation(
  location: Location = window.location,
  source: HostedRuntimeConfigWindow | null | undefined = window as HostedRuntimeConfigWindow,
): SdnBackend {
  const params = new URLSearchParams(location.search);
  const env = import.meta.env as SdnUiEnv;
  const hostedServerBaseUrl = readHostedServerBaseUrl(source);
  return createSdnBackend({
    mode: params.get('backend') ?? env.SDN_UI_BACKEND ?? (hostedServerBaseUrl ? 'remote-sdn' : 'desktop-local'),
    kuboApiUrl: params.get('api') ?? env.SDN_UI_API_URL,
    gatewayUrl: params.get('gateway') ?? env.SDN_UI_GATEWAY_URL,
    desktopProxyUrl: params.get('proxy') ?? env.SDN_UI_PROXY_TARGET ?? location.origin,
    serverUrl: params.get('server') ?? env.SDN_UI_SERVER_URL ?? hostedServerBaseUrl,
  });
}

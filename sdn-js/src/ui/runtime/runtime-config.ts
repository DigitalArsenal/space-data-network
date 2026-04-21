export interface RuntimeConfigEnv {
  VITE_SDN_DEFAULT_PROVIDER_URL?: string;
}

export interface HostedRuntimeConfigWindow {
  __SDN_CONFIG__?: {
    serverBaseUrl?: string;
    ipfsDashboardUrl?: string;
  };
}

export function readDefaultProviderDescriptorUrl(env: RuntimeConfigEnv): string | null {
  const candidate = String(env.VITE_SDN_DEFAULT_PROVIDER_URL ?? '').trim();
  if (!candidate) {
    return null;
  }

  try {
    const parsed = new URL(candidate);
    if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
      return null;
    }
    return parsed.toString();
  } catch {
    return null;
  }
}

export function readHostedServerBaseUrl(source: HostedRuntimeConfigWindow | null | undefined): string | null {
  const candidate = String(source?.__SDN_CONFIG__?.serverBaseUrl ?? '').trim();
  if (!candidate) {
    return null;
  }

  try {
    const parsed = new URL(candidate);
    if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
      return null;
    }
    return parsed.toString();
  } catch {
    return null;
  }
}

export function readHostedIPFSDashboardUrl(source: HostedRuntimeConfigWindow | null | undefined): string | null {
  const candidate = String(source?.__SDN_CONFIG__?.ipfsDashboardUrl ?? '').trim();
  if (!candidate) {
    return null;
  }

  if (candidate.startsWith('/')) {
    return candidate;
  }

  try {
    const parsed = new URL(candidate);
    if (!['http:', 'https:', 'webui:', 'sdn:'].includes(parsed.protocol)) {
      return null;
    }
    return parsed.toString();
  } catch {
    return null;
  }
}

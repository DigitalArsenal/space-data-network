export interface RuntimeConfigEnv {
  VITE_SDN_DEFAULT_PROVIDER_URL?: string;
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

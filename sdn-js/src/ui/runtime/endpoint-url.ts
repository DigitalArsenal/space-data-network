export function endpointUrlFromMultiaddr(value: string): string | null {
  const [, hostProtocol, host, transport, port] = value.split('/');
  if (!hostProtocol || !host || transport !== 'tcp' || !port) return null;
  if (!['ip4', 'ip6', 'dns', 'dns4', 'dns6'].includes(hostProtocol)) return null;
  const hostname = hostProtocol === 'ip6' ? `[${host}]` : host;
  return `http://${hostname}:${port}`;
}

export function normalizeHttpEndpointUrl(value: string | null | undefined): string | null {
  const trimmed = value?.trim();
  if (!trimmed) return null;
  const candidate = endpointUrlFromMultiaddr(trimmed) ?? trimmed;
  try {
    const url = new URL(candidate);
    if (url.protocol !== 'http:' && url.protocol !== 'https:') return null;
    return url.toString().replace(/\/+$/, '');
  } catch {
    return null;
  }
}

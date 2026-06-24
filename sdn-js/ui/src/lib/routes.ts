export type PrimaryRoute = '/node' | '/peers' | '/data' | '/channels' | '/conjunction';

export function normalizeSdnRoute(rawPath: string): string {
  const path = normalizePath(rawPath);
  if (path === '' || path === '/' || path.startsWith('/status')) return '/node';
  if (path.startsWith('/settings')) return '/node';
  if (path.startsWith('/files')) return '/data';
  if (path.startsWith('/pins')) return '/data?tab=pins';
  if (path.startsWith('/modules')) return '/peers?tab=modules';
  if (path.startsWith('/marketplace')) return '/peers?tab=marketplace';
  if (path.startsWith('/explore/')) return `/data?inspect=${encodeURIComponent(path.slice('/explore/'.length))}`;
  if (path.startsWith('/local-data')) return `/data${path.slice('/local-data'.length)}`;
  if (path.startsWith('/node') || path.startsWith('/peers') || path.startsWith('/data') || path.startsWith('/channels') || path.startsWith('/conjunction')) {
    return path;
  }
  return '/node';
}

export function primaryRouteFromNormalized(route: string): PrimaryRoute {
  if (route.startsWith('/peers')) return '/peers';
  if (route.startsWith('/data')) return '/data';
  if (route.startsWith('/channels')) return '/channels';
  if (route.startsWith('/conjunction')) return '/conjunction';
  return '/node';
}

function normalizePath(rawPath: string): string {
  const withoutHash = rawPath.startsWith('#') ? rawPath.slice(1) : rawPath;
  const withoutSdnPrefix = withoutHash.replace(/^\/sdn\/?/, '/');
  return withoutSdnPrefix || '/';
}

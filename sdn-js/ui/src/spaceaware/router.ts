/**
 * SpaceAware UI route skeleton (loop task U0.1).
 *
 * Canonical routes (served from the embedded single-file artifact by the
 * daemon — see sdn-server/cmd/spacedatanetwork/spaceaware_ui.go, which must
 * stay in sync with SPACEAWARE_ROUTES):
 *
 *   /login
 *   /console → /console/node
 *   /console/{node,peers,groups,data,channels,conjunction}
 *   /orbital
 *   /gantt
 *   /bmc2
 *   /bmc2/{f1,f2,f3,f4,f5,f6}
 *
 * Navigation uses the History API when the app is served at those paths
 * (embedded daemon serving) and falls back to hash routing when the app is
 * opened as `spaceaware.html` (Vite dev serving).
 */

export const CONSOLE_VIEWS = ['node', 'peers', 'groups', 'data', 'channels', 'conjunction'] as const;
export type ConsoleView = (typeof CONSOLE_VIEWS)[number];

export const BMC2_MODES = ['f1', 'f2', 'f3', 'f4', 'f5', 'f6'] as const;
export type Bmc2Mode = (typeof BMC2_MODES)[number];

export type SpaceAwareScreen = 'login' | 'console' | 'orbital' | 'gantt' | 'bmc2';

export interface SpaceAwareRoute {
  screen: SpaceAwareScreen;
  /** Console view or BMC2 mode; null for screens without sub-routes. */
  sub: ConsoleView | Bmc2Mode | null;
  /** Canonical path, e.g. `/console/node`. */
  path: string;
}

/** Full enumeration of canonical route paths (kept in sync with the Go side). */
export const SPACEAWARE_ROUTES: readonly string[] = [
  '/login',
  '/console',
  ...CONSOLE_VIEWS.map((view) => `/console/${view}`),
  '/orbital',
  '/gantt',
  '/bmc2',
  ...BMC2_MODES.map((mode) => `/bmc2/${mode}`),
];

function stripTrailingSlash(path: string): string {
  return path.length > 1 && path.endsWith('/') ? path.slice(0, -1) : path;
}

/** Match a location path (no query/hash) to a SpaceAware route, or null. */
export function matchSpaceAwareRoute(rawPath: string): SpaceAwareRoute | null {
  const path = stripTrailingSlash((rawPath.split('?')[0] ?? '').trim() || '/');
  if (path === '/login') {
    return { screen: 'login', sub: null, path: '/login' };
  }
  if (path === '/console') {
    return { screen: 'console', sub: 'node', path: '/console/node' };
  }
  if (path.startsWith('/console/')) {
    const view = path.slice('/console/'.length) as ConsoleView;
    if ((CONSOLE_VIEWS as readonly string[]).includes(view)) {
      return { screen: 'console', sub: view, path: `/console/${view}` };
    }
    return null;
  }
  if (path === '/orbital') {
    return { screen: 'orbital', sub: null, path: '/orbital' };
  }
  if (path === '/gantt') {
    return { screen: 'gantt', sub: null, path: '/gantt' };
  }
  if (path === '/bmc2') {
    return { screen: 'bmc2', sub: null, path: '/bmc2' };
  }
  if (path.startsWith('/bmc2/')) {
    const mode = path.slice('/bmc2/'.length) as Bmc2Mode;
    if ((BMC2_MODES as readonly string[]).includes(mode)) {
      return { screen: 'bmc2', sub: mode, path: `/bmc2/${mode}` };
    }
    return null;
  }
  return null;
}

/** True when the app was booted from spaceaware.html (Vite dev) → hash routing. */
export function useHashRouting(location: Pick<Location, 'pathname'>): boolean {
  return location.pathname.endsWith('/spaceaware.html');
}

/** Resolve the current route from a Location (pathname first, hash fallback). */
export function routeFromLocation(
  location: Pick<Location, 'pathname' | 'hash'>,
): SpaceAwareRoute {
  const fromPath = matchSpaceAwareRoute(location.pathname);
  if (fromPath && !useHashRouting(location)) {
    return fromPath;
  }
  const hashPath = location.hash.startsWith('#') ? location.hash.slice(1) : location.hash;
  const fromHash = matchSpaceAwareRoute(hashPath);
  if (fromHash) {
    return fromHash;
  }
  // Default entry: the login gate.
  return { screen: 'login', sub: null, path: '/login' };
}

export type RouteListener = (route: SpaceAwareRoute) => void;

/** Minimal history/hash router. Returns an unsubscribe function. */
export function createRouter(onChange: RouteListener): {
  navigate: (path: string) => void;
  current: () => SpaceAwareRoute;
  destroy: () => void;
} {
  const hashMode = useHashRouting(window.location);

  const emit = () => onChange(routeFromLocation(window.location));

  const onPop = () => emit();
  window.addEventListener('popstate', onPop);
  window.addEventListener('hashchange', onPop);

  return {
    navigate(path: string): void {
      const route = matchSpaceAwareRoute(path);
      const target = route?.path ?? '/login';
      if (hashMode) {
        if (window.location.hash !== `#${target}`) {
          window.location.hash = `#${target}`;
        } else {
          emit();
        }
      } else {
        if (window.location.pathname !== target) {
          window.history.pushState({}, '', target);
        }
        emit();
      }
    },
    current(): SpaceAwareRoute {
      return routeFromLocation(window.location);
    },
    destroy(): void {
      window.removeEventListener('popstate', onPop);
      window.removeEventListener('hashchange', onPop);
    },
  };
}

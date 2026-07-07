import { describe, expect, it } from 'vitest';
import {
  BMC2_MODES,
  CONSOLE_VIEWS,
  matchSpaceAwareRoute,
  routeFromLocation,
  SPACEAWARE_ROUTES,
  useHashRouting,
} from '../ui/src/spaceaware/router';

describe('SpaceAware UI route skeleton (U0.1)', () => {
  it('enumerates the full loop-doc route skeleton', () => {
    expect(SPACEAWARE_ROUTES).toEqual([
      '/login',
      '/console',
      '/console/node',
      '/console/peers',
      '/console/groups',
      '/console/data',
      '/console/channels',
      '/console/conjunction',
      '/orbital',
      '/gantt',
      '/bmc2',
      '/bmc2/f1',
      '/bmc2/f2',
      '/bmc2/f3',
      '/bmc2/f4',
      '/bmc2/f5',
      '/bmc2/f6',
    ]);
  });

  it('matches every canonical route to itself', () => {
    for (const path of SPACEAWARE_ROUTES) {
      const route = matchSpaceAwareRoute(path);
      expect(route, path).not.toBeNull();
      if (path === '/console') {
        expect(route?.path).toBe('/console/node');
      } else {
        expect(route?.path).toBe(path);
      }
    }
  });

  it('normalizes trailing slashes and query strings', () => {
    expect(matchSpaceAwareRoute('/console/peers/')?.path).toBe('/console/peers');
    expect(matchSpaceAwareRoute('/orbital?group=g1')?.path).toBe('/orbital');
    expect(matchSpaceAwareRoute('/bmc2/f4/')?.path).toBe('/bmc2/f4');
  });

  it('defaults /console to the node view', () => {
    const route = matchSpaceAwareRoute('/console');
    expect(route).toEqual({ screen: 'console', sub: 'node', path: '/console/node' });
  });

  it('rejects unknown console views and bmc2 modes', () => {
    expect(matchSpaceAwareRoute('/console/unknown')).toBeNull();
    expect(matchSpaceAwareRoute('/bmc2/f7')).toBeNull();
    expect(matchSpaceAwareRoute('/bmc2/index')).toBeNull();
  });

  it('does not claim legacy app routes', () => {
    for (const legacy of ['/', '/node', '/peers', '/data', '/channels', '/conjunction', '/admin', '/webui']) {
      expect(matchSpaceAwareRoute(legacy), legacy).toBeNull();
    }
  });

  it('covers all console views and bmc2 modes', () => {
    expect(CONSOLE_VIEWS).toEqual(['node', 'peers', 'groups', 'data', 'channels', 'conjunction']);
    expect(BMC2_MODES).toEqual(['f1', 'f2', 'f3', 'f4', 'f5', 'f6']);
  });

  it('uses hash routing only for the spaceaware.html dev entry', () => {
    expect(useHashRouting({ pathname: '/spaceaware.html' })).toBe(true);
    expect(useHashRouting({ pathname: '/login' })).toBe(false);
  });

  it('resolves routes from pathname first, hash as dev fallback, login default', () => {
    expect(routeFromLocation({ pathname: '/gantt', hash: '' }).path).toBe('/gantt');
    expect(routeFromLocation({ pathname: '/spaceaware.html', hash: '#/console/data' }).path).toBe(
      '/console/data',
    );
    expect(routeFromLocation({ pathname: '/spaceaware.html', hash: '' }).path).toBe('/login');
    expect(routeFromLocation({ pathname: '/unknown', hash: '' }).path).toBe('/login');
  });
});

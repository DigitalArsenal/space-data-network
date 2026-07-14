import { describe, expect, it } from 'vitest';
import {
  CONSOLE_NAV_ITEMS,
  CONSOLE_RAIL_PIN_STORAGE_KEY,
  CONSOLE_SUBTITLES,
  CONSOLE_TITLES,
  NODE_DEFAULT_LAYOUT,
  NODE_LAYOUT_STORAGE_KEY,
  NODE_PEER_SUMMARY_PLACEHOLDER,
  NODE_THROUGHPUT_SPARK,
  NODE_WIDGETS,
  NODE_WIDGET_ORDER,
  addNodeWidget,
  availableNodeWidgets,
  cloneNodeLayout,
  consoleHealthChipState,
  consoleHealthChipStyle,
  consoleNavItemStyle,
  consoleNavItemsForGroup,
  consoleSessionChipStyle,
  consoleTitleAccent,
  cycleWidgetSpan,
  generateQrPlaceholderPattern,
  hexToRgba,
  isValidNodeLayout,
  loadNodeLayout,
  loadRailPinned,
  nodeMapTabStyle,
  parseConsoleDeepLinkQuery,
  peerTrustColor,
  removeNodeWidget,
  reorderNodeLayout,
  resetNodeLayout,
  resolveConsoleDeepLinkPath,
  saveNodeLayout,
  saveRailPinned,
  throughputBarGradient,
  widgetSpanLabel,
  type NodeLayout,
} from './console';
import { CONSOLE_VIEWS } from '../router';

// ---------------------------------------------------------------------------
// In-memory Storage stub (jsdom-free — vitest here runs with `environment:
// 'node'`, see root vitest.config.mts).
// ---------------------------------------------------------------------------

function memoryStorage(initial: Record<string, string> = {}): Storage {
  const data = new Map(Object.entries(initial));
  return {
    getItem: (key: string) => (data.has(key) ? data.get(key)! : null),
    setItem: (key: string, value: string) => {
      data.set(key, value);
    },
    removeItem: (key: string) => {
      data.delete(key);
    },
    clear: () => data.clear(),
    key: (index: number) => Array.from(data.keys())[index] ?? null,
    get length() {
      return data.size;
    },
  } as Storage;
}

function throwingStorage(): Storage {
  return {
    getItem: () => {
      throw new Error('storage unavailable');
    },
    setItem: () => {
      throw new Error('storage unavailable');
    },
    removeItem: () => {},
    clear: () => {},
    key: () => null,
    length: 0,
  } as unknown as Storage;
}

// ---------------------------------------------------------------------------
// Nav model
// ---------------------------------------------------------------------------

describe('CONSOLE_NAV_ITEMS', () => {
  it('covers all six console views, in ROUTES order', () => {
    expect(CONSOLE_NAV_ITEMS.map((i) => i.id)).toEqual(CONSOLE_VIEWS);
  });

  it('splits into identity (node/peers/groups) and operations (data/channels/conjunction) groups', () => {
    expect(consoleNavItemsForGroup('identity').map((i) => i.id)).toEqual(['node', 'peers', 'groups']);
    expect(consoleNavItemsForGroup('operations').map((i) => i.id)).toEqual(['data', 'channels', 'conjunction']);
  });
});

describe('consoleNavItemStyle', () => {
  it('gives the active item a cyan accent, inactive items no accent', () => {
    const active = consoleNavItemStyle({ id: 'node' }, 'node');
    expect(active.barColor).toBe('#35c9d8');
    expect(active.background).toBe('rgba(74,166,224,0.1)');
    const inactive = consoleNavItemStyle({ id: 'peers' }, 'node');
    expect(inactive.barColor).toBe('transparent');
    expect(inactive.background).toBe('transparent');
  });

  it('gives CONJUNCTION a red accent only when active', () => {
    expect(consoleNavItemStyle({ id: 'conjunction' }, 'conjunction').barColor).toBe('#ff6b6b');
    expect(consoleNavItemStyle({ id: 'conjunction' }, 'node').barColor).toBe('transparent');
  });
});

describe('titles', () => {
  it('CONSOLE_TITLES and CONSOLE_SUBTITLES cover every view', () => {
    const viewSet = new Set(CONSOLE_VIEWS);
    expect(new Set(Object.keys(CONSOLE_TITLES))).toEqual(viewSet);
    expect(new Set(Object.keys(CONSOLE_SUBTITLES))).toEqual(viewSet);
  });

  it('titleAccent is red for conjunction, neutral otherwise', () => {
    expect(consoleTitleAccent('conjunction')).toBe('#d68a8a');
    for (const view of CONSOLE_VIEWS) {
      if (view === 'conjunction') continue;
      expect(consoleTitleAccent(view)).toBe('#5d7681');
    }
  });
});

// ---------------------------------------------------------------------------
// Deep-link mapping
// ---------------------------------------------------------------------------

describe('parseConsoleDeepLinkQuery', () => {
  it('parses a recognized ?route= value', () => {
    expect(parseConsoleDeepLinkQuery('?route=groups').view).toBe('groups');
  });

  it('rejects an unrecognized ?route= value', () => {
    expect(parseConsoleDeepLinkQuery('?route=bogus').view).toBeNull();
  });

  it('is null when ?route= is absent', () => {
    expect(parseConsoleDeepLinkQuery('').view).toBeNull();
    expect(parseConsoleDeepLinkQuery('?group=leo-a').view).toBeNull();
  });

  it('captures and trims ?group=', () => {
    expect(parseConsoleDeepLinkQuery('?group=%20leo-a%20').group).toBe('leo-a');
    expect(parseConsoleDeepLinkQuery('?group=').group).toBeNull();
    expect(parseConsoleDeepLinkQuery('').group).toBeNull();
  });

  it('parses both params together', () => {
    expect(parseConsoleDeepLinkQuery('?route=conjunction&group=leo-a')).toEqual({
      view: 'conjunction',
      group: 'leo-a',
    });
  });
});

describe('resolveConsoleDeepLinkPath', () => {
  it('returns the target path when ?route= differs from the current view', () => {
    expect(resolveConsoleDeepLinkPath('?route=groups', 'node')).toBe('/console/groups');
  });

  it('returns null when ?route= matches the current view (no redundant navigation)', () => {
    expect(resolveConsoleDeepLinkPath('?route=node', 'node')).toBeNull();
  });

  it('returns null when there is no ?route=, or it is unrecognized', () => {
    expect(resolveConsoleDeepLinkPath('', 'node')).toBeNull();
    expect(resolveConsoleDeepLinkPath('?route=bogus', 'node')).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// Rail pin persistence
// ---------------------------------------------------------------------------

describe('rail pin persistence', () => {
  it('defaults to unpinned when storage is empty/unavailable', () => {
    expect(loadRailPinned(memoryStorage())).toBe(false);
    expect(loadRailPinned(null)).toBe(false);
    expect(loadRailPinned(throwingStorage())).toBe(false);
  });

  it('round-trips true/false through the storage key', () => {
    const storage = memoryStorage();
    saveRailPinned(storage, true);
    expect(storage.getItem(CONSOLE_RAIL_PIN_STORAGE_KEY)).toBe('1');
    expect(loadRailPinned(storage)).toBe(true);
    saveRailPinned(storage, false);
    expect(loadRailPinned(storage)).toBe(false);
  });

  it('never throws when storage.setItem throws', () => {
    expect(() => saveRailPinned(throwingStorage(), true)).not.toThrow();
  });
});

// ---------------------------------------------------------------------------
// Header chips
// ---------------------------------------------------------------------------

describe('consoleHealthChipState / consoleHealthChipStyle', () => {
  it('maps NOMINAL/DEGRADED/ALERT to ONLINE/DEGRADED/OFFLINE', () => {
    expect(consoleHealthChipState('NOMINAL')).toBe('ONLINE');
    expect(consoleHealthChipState('DEGRADED')).toBe('DEGRADED');
    expect(consoleHealthChipState('ALERT')).toBe('OFFLINE');
  });

  it('gives each state its own color', () => {
    expect(consoleHealthChipStyle('ONLINE').color).toBe('#5ad6a0');
    expect(consoleHealthChipStyle('DEGRADED').color).toBe('#ffb24d');
    expect(consoleHealthChipStyle('OFFLINE').color).toBe('#ff6b6b');
  });
});

describe('consoleSessionChipStyle', () => {
  it('labels every AuthStatus', () => {
    expect(consoleSessionChipStyle('authenticated').label).toBe('IDENTITY CONFIRMED');
    expect(consoleSessionChipStyle('anonymous').label).toBe('ANONYMOUS SESSION');
    expect(consoleSessionChipStyle('authenticating').label).toBe('AUTHENTICATING…');
    expect(consoleSessionChipStyle('error').label).toBe('SESSION ERROR');
    expect(consoleSessionChipStyle('unknown').label).toBe('CHECKING SESSION…');
  });
});

// ---------------------------------------------------------------------------
// QR placeholder pattern
// ---------------------------------------------------------------------------

describe('generateQrPlaceholderPattern', () => {
  it('produces size*size deterministic booleans', () => {
    const a = generateQrPlaceholderPattern(11);
    const b = generateQrPlaceholderPattern(11);
    expect(a).toHaveLength(121);
    expect(a).toEqual(b);
  });

  it('draws solid finder-pattern borders in the three corners', () => {
    const cells = generateQrPlaceholderPattern(11);
    const at = (r: number, c: number) => cells[r * 11 + c];
    // Top-left finder ring corners.
    expect(at(0, 0)).toBe(true);
    expect(at(2, 2)).toBe(true);
    expect(at(1, 1)).toBe(true); // finder eye
    // Top-right + bottom-left finder patterns are also present.
    expect(at(0, 10)).toBe(true);
    expect(at(10, 0)).toBe(true);
    // Center of the top-left finder eye is a fixed corner, but a
    // non-finder, non-edge cell is pseudo-random rather than always on.
    expect(cells.some((c) => c === false)).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// NODE widget catalog + layout engine
// ---------------------------------------------------------------------------

describe('NODE_WIDGETS / NODE_WIDGET_ORDER', () => {
  it('catalog covers every widget id exactly once', () => {
    expect(new Set(NODE_WIDGET_ORDER).size).toBe(NODE_WIDGET_ORDER.length);
    expect(Object.keys(NODE_WIDGETS).sort()).toEqual([...NODE_WIDGET_ORDER].sort());
  });

  it('every default-layout entry references a real widget with a valid default span', () => {
    for (const entry of NODE_DEFAULT_LAYOUT) {
      const spec = NODE_WIDGETS[entry.id];
      expect(spec).toBeDefined();
      expect(spec.spans).toContain(entry.span);
    }
  });
});

describe('isValidNodeLayout', () => {
  it('accepts a well-formed layout', () => {
    expect(isValidNodeLayout(NODE_DEFAULT_LAYOUT)).toBe(true);
  });

  it('rejects empty arrays, non-arrays, and unknown widget ids', () => {
    expect(isValidNodeLayout([])).toBe(false);
    expect(isValidNodeLayout(null)).toBe(false);
    expect(isValidNodeLayout('nope')).toBe(false);
    expect(isValidNodeLayout([{ id: 'not-a-widget', span: 4 }])).toBe(false);
    expect(isValidNodeLayout([{ id: 'health', span: '4' }])).toBe(false);
  });
});

describe('loadNodeLayout / saveNodeLayout', () => {
  it('falls back to NODE_DEFAULT_LAYOUT when storage is empty/unavailable/corrupt', () => {
    expect(loadNodeLayout(memoryStorage())).toEqual(NODE_DEFAULT_LAYOUT);
    expect(loadNodeLayout(null)).toEqual(NODE_DEFAULT_LAYOUT);
    expect(loadNodeLayout(throwingStorage())).toEqual(NODE_DEFAULT_LAYOUT);
    expect(loadNodeLayout(memoryStorage({ [NODE_LAYOUT_STORAGE_KEY]: '{not json' }))).toEqual(NODE_DEFAULT_LAYOUT);
    expect(
      loadNodeLayout(memoryStorage({ [NODE_LAYOUT_STORAGE_KEY]: JSON.stringify([{ id: 'bogus', span: 4 }]) })),
    ).toEqual(NODE_DEFAULT_LAYOUT);
  });

  it('round-trips a custom layout through localStorage', () => {
    const storage = memoryStorage();
    const custom: NodeLayout = [
      { id: 'activity', span: 12 },
      { id: 'storage', span: 4 },
    ];
    saveNodeLayout(storage, custom);
    expect(loadNodeLayout(storage)).toEqual(custom);
  });

  it('never throws when storage.setItem throws', () => {
    expect(() => saveNodeLayout(throwingStorage(), NODE_DEFAULT_LAYOUT)).not.toThrow();
  });

  it("loaded/reset layouts are independent copies (mutating one doesn't affect NODE_DEFAULT_LAYOUT)", () => {
    const loaded = loadNodeLayout(memoryStorage());
    loaded[0]!.span = 999;
    expect(NODE_DEFAULT_LAYOUT[0]!.span).not.toBe(999);
  });
});

describe('reorderNodeLayout', () => {
  it('moves the dragged widget to the target position', () => {
    const layout = cloneNodeLayout(NODE_DEFAULT_LAYOUT); // health, identity, service, netmap, throughput
    const next = reorderNodeLayout(layout, 'throughput', 'health');
    expect(next.map((w) => w.id)).toEqual(['throughput', 'health', 'identity', 'service', 'netmap']);
  });

  it('is a no-op when dragged === target, or either id is missing', () => {
    const layout = cloneNodeLayout(NODE_DEFAULT_LAYOUT);
    expect(reorderNodeLayout(layout, 'health', 'health')).toBe(layout);
    expect(reorderNodeLayout(layout, 'peersum', 'health')).toBe(layout);
    expect(reorderNodeLayout(layout, 'health', 'peersum')).toBe(layout);
  });
});

describe('cycleWidgetSpan', () => {
  it('advances through the widget spans list and wraps around', () => {
    let layout: NodeLayout = [{ id: 'health', span: 4 }]; // spans: [4, 6]
    layout = cycleWidgetSpan(layout, 'health');
    expect(layout[0]!.span).toBe(6);
    layout = cycleWidgetSpan(layout, 'health');
    expect(layout[0]!.span).toBe(4);
  });

  it('cycles a 3-span widget (netmap: 6 -> 8 -> 12 -> 6)', () => {
    let layout: NodeLayout = [{ id: 'netmap', span: 6 }];
    layout = cycleWidgetSpan(layout, 'netmap');
    expect(layout[0]!.span).toBe(8);
    layout = cycleWidgetSpan(layout, 'netmap');
    expect(layout[0]!.span).toBe(12);
    layout = cycleWidgetSpan(layout, 'netmap');
    expect(layout[0]!.span).toBe(6);
  });

  it('leaves other widgets untouched', () => {
    const layout: NodeLayout = [
      { id: 'health', span: 4 },
      { id: 'identity', span: 4 },
    ];
    const next = cycleWidgetSpan(layout, 'health');
    expect(next[1]).toEqual({ id: 'identity', span: 4 });
  });
});

describe('removeNodeWidget / addNodeWidget / availableNodeWidgets', () => {
  it('removes exactly the targeted widget', () => {
    const layout = cloneNodeLayout(NODE_DEFAULT_LAYOUT);
    const next = removeNodeWidget(layout, 'service');
    expect(next.map((w) => w.id)).toEqual(['health', 'identity', 'netmap', 'throughput']);
  });

  it('adds a widget at its default span, appended to the end', () => {
    const layout = cloneNodeLayout(NODE_DEFAULT_LAYOUT);
    const next = addNodeWidget(layout, 'storage');
    expect(next.at(-1)).toEqual({ id: 'storage', span: NODE_WIDGETS.storage.defaultSpan });
  });

  it('adding an already-present widget is a no-op', () => {
    const layout = cloneNodeLayout(NODE_DEFAULT_LAYOUT);
    expect(addNodeWidget(layout, 'health')).toBe(layout);
  });

  it('availableNodeWidgets excludes widgets already in the layout, preserving catalog order', () => {
    const layout: NodeLayout = [{ id: 'health', span: 4 }];
    const available = availableNodeWidgets(layout).map((w) => w.id);
    expect(available).toEqual([
      'identity',
      'service',
      'netmap',
      'throughput',
      'peersum',
      'storage',
      'activity',
      'credentials',
    ]);
  });

  it('availableNodeWidgets is empty once every widget is placed', () => {
    const full: NodeLayout = NODE_WIDGET_ORDER.map((id) => ({ id, span: NODE_WIDGETS[id].defaultSpan }));
    expect(availableNodeWidgets(full)).toEqual([]);
  });
});

describe('resetNodeLayout', () => {
  it('returns a copy equal to NODE_DEFAULT_LAYOUT, not the same reference', () => {
    const reset = resetNodeLayout();
    expect(reset).toEqual(NODE_DEFAULT_LAYOUT);
    expect(reset).not.toBe(NODE_DEFAULT_LAYOUT);
  });
});

describe('widgetSpanLabel', () => {
  it('formats as W<span>', () => {
    expect(widgetSpanLabel(4)).toBe('W4');
    expect(widgetSpanLabel(12)).toBe('W12');
  });
});

// ---------------------------------------------------------------------------
// Widget placeholder datasets
// ---------------------------------------------------------------------------

describe('peerTrustColor', () => {
  it('maps trusted/observed/unknown to distinct colors', () => {
    expect(peerTrustColor('trusted')).toBe('#5ad6a0');
    expect(peerTrustColor('observed')).toBe('#7d929b');
    expect(peerTrustColor('unknown')).toBe('#ffb24d');
    expect(peerTrustColor('anything-else')).toBe('#ffb24d');
  });
});

describe('NODE_PEER_SUMMARY_PLACEHOLDER', () => {
  it('has exactly 3 rows matching the mock PEERS.slice(0,3)', () => {
    expect(NODE_PEER_SUMMARY_PLACEHOLDER).toHaveLength(3);
    expect(NODE_PEER_SUMMARY_PLACEHOLDER.map((r) => r.name)).toEqual([
      'SpaceAware.io',
      'CelesTrak Provider',
      'OrbitalEdge Node',
    ]);
  });
});

describe('NODE_THROUGHPUT_SPARK / throughputBarGradient', () => {
  it('has 12 bars', () => {
    expect(NODE_THROUGHPUT_SPARK).toHaveLength(12);
  });

  it('bar index 8 gets the highlighted gradient, others the standard one', () => {
    expect(throughputBarGradient(8)).toContain('#4aa6e0');
    expect(throughputBarGradient(0)).toContain('#35c9d8');
    expect(throughputBarGradient(11)).toContain('#35c9d8');
  });
});

describe('hexToRgba', () => {
  it('converts a 6-digit hex color at the given alpha', () => {
    expect(hexToRgba('#5ad6a0', 0.4)).toBe('rgba(90,214,160,0.4)');
    expect(hexToRgba('5ad6a0', 0.06)).toBe('rgba(90,214,160,0.06)');
  });

  it('falls back to a neutral gray for malformed input, never throws', () => {
    expect(() => hexToRgba('not-a-color', 0.4)).not.toThrow();
    expect(hexToRgba('not-a-color', 0.4)).toBe('rgba(125,146,155,0.4)');
  });
});

describe('nodeMapTabStyle', () => {
  it('treats anything but "2d" as 3D (the mock default)', () => {
    expect(nodeMapTabStyle('3d').color3d).toBe('#9fe9f2');
    expect(nodeMapTabStyle('3d').color2d).toBe('#7d929b');
    expect(nodeMapTabStyle('2d').color2d).toBe('#9fe9f2');
    expect(nodeMapTabStyle('2d').color3d).toBe('#7d929b');
  });
});

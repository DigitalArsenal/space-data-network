import { describe, expect, it } from 'vitest';
import {
  GROUPS_CONJUNCTION_CONSOLE_PATH,
  GROUPS_CONJUNCTION_DEMO_TAG_TITLE,
  GROUPS_CREATE_NAME_REQUIRED_MESSAGE,
  GROUPS_LEGEND_CAPTION,
  GROUPS_OWNERSHIP_DEMO_TAG_TITLE,
  GROUPS_READ_ONLY_NOTE,
  GROUPS_STORAGE_KEY,
  GROUP_FILTER_TABS,
  GROUP_PRIMITIVES,
  GROUP_REGIME_OPTIONS,
  SEED_GROUPS,
  buildConjunctionEvents,
  buildGroupConjunctionCell,
  buildGroupConjunctionSection,
  buildGroupDetailView,
  buildGroupRows,
  canDeleteGroup,
  cloneGroups,
  conjColor,
  conjLabel,
  createGroup,
  deleteGroup,
  eventStateColor,
  filterGroups,
  formatEventTca,
  formatUpdatedLabel,
  generateGroupId,
  groupColor,
  groupFilterTabStyle,
  groupGlyph,
  groupOrbitalPath,
  groupsCountCaption,
  isConjunctionDemo,
  isPeerOwnershipDemo,
  isValidSharedGroup,
  isValidSharedGroupList,
  loadSharedGroups,
  resolveDeepLinkGroupId,
  resolveSelectedGroup,
  saveSharedGroups,
  validateCreateGroupInput,
  type SharedGroup,
} from './groups-data';

// ---------------------------------------------------------------------------
// In-memory Storage stub (mirrors console.test.ts's helper — vitest here
// runs with `environment: 'node'`, no real localStorage available).
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

function group(overrides: Partial<SharedGroup> = {}): SharedGroup {
  return {
    id: 'leo-a',
    name: 'LEO Constellation A',
    owner: 'self',
    ownerName: 'THIS NODE',
    count: 42,
    regime: 'LEO',
    scope: '53° shell · operated assets',
    conj: 'watch',
    conjN: 2,
    maxPc: '7.3e-5',
    nextTca: '19h 40m',
    tcaH: 19.7,
    updated: '2m ago',
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// SEED_GROUPS / static fixtures
// ---------------------------------------------------------------------------

describe('SEED_GROUPS', () => {
  it('has 6 fixtures — 3 owner:self, 3 peer/provider', () => {
    expect(SEED_GROUPS).toHaveLength(6);
    expect(SEED_GROUPS.filter((g) => g.owner === 'self')).toHaveLength(3);
    expect(SEED_GROUPS.filter((g) => g.owner !== 'self')).toHaveLength(3);
  });

  it('every id is unique', () => {
    const ids = new Set(SEED_GROUPS.map((g) => g.id));
    expect(ids.size).toBe(SEED_GROUPS.length);
  });

  it('GROUP_PRIMITIVES has the 4 legend entries in mock order', () => {
    expect(GROUP_PRIMITIVES.map((p) => p.id)).toEqual(['self', 'peer', 'mygroup', 'peergroup']);
  });

  it('GROUP_FILTER_TABS / GROUP_REGIME_OPTIONS match the mock vocabulary', () => {
    expect(GROUP_FILTER_TABS.map((t) => t.id)).toEqual(['all', 'mine', 'peer']);
    expect(GROUP_REGIME_OPTIONS).toEqual(['ALL', 'LEO', 'MEO', 'GEO', 'MIXED']);
  });

  it('exposes the legend caption text verbatim', () => {
    expect(GROUPS_LEGEND_CAPTION).toContain('administered here');
  });
});

// ---------------------------------------------------------------------------
// cloneGroups
// ---------------------------------------------------------------------------

describe('cloneGroups', () => {
  it('returns a deep-enough copy — mutating a clone does not affect the source', () => {
    const clone = cloneGroups(SEED_GROUPS);
    clone[0]!.name = 'mutated';
    expect(SEED_GROUPS[0]!.name).not.toBe('mutated');
  });
});

// ---------------------------------------------------------------------------
// isValidSharedGroup / isValidSharedGroupList
// ---------------------------------------------------------------------------

describe('isValidSharedGroup', () => {
  it('accepts a well-formed group', () => {
    expect(isValidSharedGroup(group())).toBe(true);
  });

  it('rejects a non-object', () => {
    expect(isValidSharedGroup(null)).toBe(false);
    expect(isValidSharedGroup('leo-a')).toBe(false);
    expect(isValidSharedGroup(42)).toBe(false);
    expect(isValidSharedGroup([])).toBe(false);
  });

  it('rejects a missing field', () => {
    const { name: _name, ...rest } = group();
    expect(isValidSharedGroup(rest)).toBe(false);
  });

  it('rejects a wrong-typed field (count as string)', () => {
    expect(isValidSharedGroup({ ...group(), count: '42' })).toBe(false);
  });

  it('rejects a blank id', () => {
    expect(isValidSharedGroup({ ...group(), id: '' })).toBe(false);
  });
});

describe('isValidSharedGroupList', () => {
  it('accepts an array of valid groups (including empty)', () => {
    expect(isValidSharedGroupList([])).toBe(true);
    expect(isValidSharedGroupList([group(), group({ id: 'x' })])).toBe(true);
  });

  it('rejects a non-array or an array with one invalid entry', () => {
    expect(isValidSharedGroupList({})).toBe(false);
    expect(isValidSharedGroupList([group(), { id: 'bad' }])).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// loadSharedGroups / saveSharedGroups — storage round-trip, corrupted JSON
// recovery, seed-on-empty
// ---------------------------------------------------------------------------

describe('loadSharedGroups', () => {
  it('seeds SEED_GROUPS when the key is missing', () => {
    const result = loadSharedGroups(memoryStorage());
    expect(result).toEqual(SEED_GROUPS);
  });

  it('does not eagerly persist the seed back to storage', () => {
    const storage = memoryStorage();
    loadSharedGroups(storage);
    expect(storage.getItem(GROUPS_STORAGE_KEY)).toBeNull();
  });

  it('round-trips a previously saved list', () => {
    const storage = memoryStorage();
    const custom = [group({ id: 'custom-1', name: 'Custom Group' })];
    saveSharedGroups(storage, custom);
    expect(loadSharedGroups(storage)).toEqual(custom);
  });

  it('falls back to seeds on corrupted JSON without throwing', () => {
    const storage = memoryStorage({ [GROUPS_STORAGE_KEY]: '{not json' });
    expect(() => loadSharedGroups(storage)).not.toThrow();
    expect(loadSharedGroups(storage)).toEqual(SEED_GROUPS);
  });

  it('falls back to seeds when the stored payload has the wrong shape', () => {
    const storage = memoryStorage({ [GROUPS_STORAGE_KEY]: JSON.stringify([{ id: 'bad' }]) });
    expect(loadSharedGroups(storage)).toEqual(SEED_GROUPS);
  });

  it('falls back to seeds when storage itself throws', () => {
    expect(() => loadSharedGroups(throwingStorage())).not.toThrow();
    expect(loadSharedGroups(throwingStorage())).toEqual(SEED_GROUPS);
  });

  it('falls back to seeds for null/undefined storage', () => {
    expect(loadSharedGroups(null)).toEqual(SEED_GROUPS);
    expect(loadSharedGroups(undefined)).toEqual(SEED_GROUPS);
  });
});

describe('saveSharedGroups', () => {
  it('serializes the exact list passed in', () => {
    const storage = memoryStorage();
    saveSharedGroups(storage, [group()]);
    expect(JSON.parse(storage.getItem(GROUPS_STORAGE_KEY)!)).toEqual([group()]);
  });

  it('does not throw when storage is unavailable', () => {
    expect(() => saveSharedGroups(throwingStorage(), [group()])).not.toThrow();
    expect(() => saveSharedGroups(null, [group()])).not.toThrow();
  });
});

// ---------------------------------------------------------------------------
// Schema shape stability — exact serialized field-name set (so a later loop
// task's Orbital screen can rely on this shape)
// ---------------------------------------------------------------------------

describe('schema shape stability', () => {
  const EXPECTED_FIELDS = [
    'conj',
    'conjN',
    'count',
    'id',
    'maxPc',
    'name',
    'nextTca',
    'owner',
    'ownerName',
    'regime',
    'scope',
    'tcaH',
    'updated',
  ];

  it('every SEED_GROUPS entry serializes to exactly this field set', () => {
    for (const g of SEED_GROUPS) {
      expect(Object.keys(g).sort()).toEqual(EXPECTED_FIELDS);
    }
  });

  it('a round-tripped (save→load) group keeps the exact same field set', () => {
    const storage = memoryStorage();
    saveSharedGroups(storage, [group()]);
    const loaded = loadSharedGroups(storage);
    expect(Object.keys(loaded[0]!).sort()).toEqual(EXPECTED_FIELDS);
  });

  it('a created group also serializes to exactly this field set', () => {
    const created = createGroup([], { name: 'New Group', regime: 'LEO', scope: 'test scope' });
    expect(Object.keys(created[0]!).sort()).toEqual(EXPECTED_FIELDS);
  });
});

// ---------------------------------------------------------------------------
// generateGroupId / validateCreateGroupInput / createGroup
// ---------------------------------------------------------------------------

describe('generateGroupId', () => {
  it('slugifies the name', () => {
    expect(generateGroupId('New Watch Group', [])).toBe('new-watch-group');
  });

  it('strips non-alphanumeric characters', () => {
    expect(generateGroupId('GEO 75°E – 105°E!!', [])).toBe('geo-75-e-105-e');
  });

  it('falls back to "group" for an unsluggable name', () => {
    expect(generateGroupId('°°°', [])).toBe('group');
  });

  it('disambiguates a collision with -2, -3, …', () => {
    const existing = [group({ id: 'watch-group' }), group({ id: 'watch-group-2' })];
    expect(generateGroupId('Watch Group', existing)).toBe('watch-group-3');
  });
});

describe('validateCreateGroupInput', () => {
  it('rejects a blank/whitespace-only name', () => {
    expect(validateCreateGroupInput({ name: '', regime: 'LEO', scope: '' })).toBe(GROUPS_CREATE_NAME_REQUIRED_MESSAGE);
    expect(validateCreateGroupInput({ name: '   ', regime: 'LEO', scope: '' })).toBe(GROUPS_CREATE_NAME_REQUIRED_MESSAGE);
  });

  it('accepts a non-blank name', () => {
    expect(validateCreateGroupInput({ name: 'My Group', regime: 'LEO', scope: '' })).toBeNull();
  });
});

describe('createGroup', () => {
  it('appends a new owner:self group with honest zeroed fields', () => {
    const now = new Date('2026-07-11T12:00:00Z');
    const result = createGroup(SEED_GROUPS, { name: 'My New Group', regime: 'LEO', scope: 'test scope' }, now);
    expect(result).toHaveLength(SEED_GROUPS.length + 1);
    const created = result[result.length - 1]!;
    expect(created).toMatchObject({
      id: 'my-new-group',
      name: 'My New Group',
      owner: 'self',
      ownerName: 'THIS NODE',
      count: 0,
      regime: 'LEO',
      scope: 'test scope',
      conj: '',
      conjN: 0,
      updated: '2026-07-11T12:00:00.000Z',
    });
  });

  it('defaults a blank regime to ALL', () => {
    const created = createGroup([], { name: 'X', regime: '', scope: '' })[0]!;
    expect(created.regime).toBe('ALL');
  });

  it('is a no-op for a blank name', () => {
    expect(createGroup(SEED_GROUPS, { name: '  ', regime: 'LEO', scope: '' })).toEqual(SEED_GROUPS);
  });

  it('does not mutate the input array', () => {
    const before = [...SEED_GROUPS];
    createGroup(SEED_GROUPS, { name: 'X', regime: 'LEO', scope: '' });
    expect(SEED_GROUPS).toEqual(before);
  });
});

// ---------------------------------------------------------------------------
// canDeleteGroup / deleteGroup
// ---------------------------------------------------------------------------

describe('canDeleteGroup', () => {
  it('true for owner:self, false otherwise', () => {
    expect(canDeleteGroup(group({ owner: 'self' }))).toBe(true);
    expect(canDeleteGroup(group({ owner: 'celestrak' }))).toBe(false);
  });
});

describe('deleteGroup', () => {
  it('removes a owner:self group by id', () => {
    const groups = [group({ id: 'leo-a', owner: 'self' }), group({ id: 'geo-watch', owner: 'self' })];
    expect(deleteGroup(groups, 'leo-a').map((g) => g.id)).toEqual(['geo-watch']);
  });

  it('is a no-op for a peer/provider group id (immutable)', () => {
    const groups = [group({ id: 'ct-active', owner: 'celestrak' })];
    expect(deleteGroup(groups, 'ct-active')).toEqual(groups);
  });

  it('is a no-op for an id that is not present', () => {
    const groups = [group({ id: 'leo-a' })];
    expect(deleteGroup(groups, 'does-not-exist')).toEqual(groups);
  });
});

// ---------------------------------------------------------------------------
// filterGroups / groupsCountCaption
// ---------------------------------------------------------------------------

describe('filterGroups', () => {
  it('"all" returns every group', () => {
    expect(filterGroups(SEED_GROUPS, 'all')).toEqual(SEED_GROUPS);
  });

  it('"mine" returns only owner:self groups', () => {
    expect(filterGroups(SEED_GROUPS, 'mine').every((g) => g.owner === 'self')).toBe(true);
    expect(filterGroups(SEED_GROUPS, 'mine')).toHaveLength(3);
  });

  it('"peer" returns only non-self groups', () => {
    expect(filterGroups(SEED_GROUPS, 'peer').every((g) => g.owner !== 'self')).toBe(true);
    expect(filterGroups(SEED_GROUPS, 'peer')).toHaveLength(3);
  });
});

describe('groupsCountCaption', () => {
  it('counts against the full (unfiltered) set', () => {
    expect(groupsCountCaption(SEED_GROUPS)).toEqual({ mineCount: 3, peerCount: 3 });
  });

  it('handles an empty list', () => {
    expect(groupsCountCaption([])).toEqual({ mineCount: 0, peerCount: 0 });
  });
});

// ---------------------------------------------------------------------------
// groupFilterTabStyle
// ---------------------------------------------------------------------------

describe('groupFilterTabStyle', () => {
  it('active tab gets the ice-blue accent', () => {
    expect(groupFilterTabStyle('mine', 'mine')).toEqual({
      color: '#9fd4f5',
      border: 'rgba(120,190,230,0.5)',
      background: 'rgba(74,166,224,0.1)',
    });
  });

  it('inactive tab stays neutral gray', () => {
    expect(groupFilterTabStyle('peer', 'mine')).toEqual({
      color: '#7d929b',
      border: 'rgba(90,150,180,0.28)',
      background: 'transparent',
    });
  });
});

// ---------------------------------------------------------------------------
// groupGlyph / groupColor
// ---------------------------------------------------------------------------

describe('groupGlyph / groupColor', () => {
  it('owner:self → filled hexagon, purple', () => {
    expect(groupGlyph({ owner: 'self' })).toBe('⬢');
    expect(groupColor({ owner: 'self' })).toBe('#c77dff');
  });

  it('any other owner → outline hexagon, amber', () => {
    expect(groupGlyph({ owner: 'celestrak' })).toBe('⬡');
    expect(groupColor({ owner: 'obs1' })).toBe('#ff9e64');
  });
});

// ---------------------------------------------------------------------------
// conjColor / conjLabel
// ---------------------------------------------------------------------------

describe('conjColor', () => {
  it('maps the 3 known statuses', () => {
    expect(conjColor('clear')).toBe('#5ad6a0');
    expect(conjColor('watch')).toBe('#ffb24d');
    expect(conjColor('critical')).toBe('#ff6b6b');
  });

  it('degrades to neutral gray for an unknown value', () => {
    expect(conjColor('')).toBe('#9fb3bc');
    expect(conjColor('bogus')).toBe('#9fb3bc');
  });
});

describe('conjLabel', () => {
  it('maps the 3 known statuses', () => {
    expect(conjLabel('clear')).toBe('CLEAR');
    expect(conjLabel('watch')).toBe('WATCH');
    expect(conjLabel('critical')).toBe('CRITICAL');
  });

  it('renders an empty status as a dash', () => {
    expect(conjLabel('')).toBe('—');
  });

  it('uppercases an unrecognized non-empty value rather than guessing', () => {
    expect(conjLabel('bogus')).toBe('BOGUS');
  });
});

// ---------------------------------------------------------------------------
// isPeerOwnershipDemo / isConjunctionDemo
// ---------------------------------------------------------------------------

describe('isPeerOwnershipDemo', () => {
  it('false for owner:self, true otherwise', () => {
    expect(isPeerOwnershipDemo({ owner: 'self' })).toBe(false);
    expect(isPeerOwnershipDemo({ owner: 'celestrak' })).toBe(true);
  });
});

describe('isConjunctionDemo', () => {
  it('true when conj is set, false when empty', () => {
    expect(isConjunctionDemo({ conj: 'watch' })).toBe(true);
    expect(isConjunctionDemo({ conj: '' })).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// buildGroupConjunctionCell (directory column)
// ---------------------------------------------------------------------------

describe('buildGroupConjunctionCell', () => {
  it('renders a fixture status with its dot color, label, and count suffix', () => {
    expect(buildGroupConjunctionCell({ conj: 'watch', conjN: 5 })).toEqual({
      hasData: true,
      dotColor: '#ffb24d',
      label: 'WATCH',
      countSuffix: '· 5',
    });
  });

  it('renders an honest dash with no count for conj:""', () => {
    expect(buildGroupConjunctionCell({ conj: '', conjN: 0 })).toEqual({
      hasData: false,
      dotColor: '#5a7a8a',
      label: '—',
      countSuffix: '',
    });
  });
});

// ---------------------------------------------------------------------------
// buildGroupRows
// ---------------------------------------------------------------------------

describe('buildGroupRows', () => {
  it('maps every group to a row view with the expected fields', () => {
    const rows = buildGroupRows([group()], 'leo-a');
    expect(rows).toEqual([
      {
        id: 'leo-a',
        name: 'LEO Constellation A',
        regimeScope: 'LEO · 53° shell · operated assets',
        glyph: '⬢',
        glyphColor: '#c77dff',
        ownerName: 'THIS NODE',
        ownerColor: '#c77dff',
        countLabel: '42 OBJ',
        conj: { hasData: true, dotColor: '#ffb24d', label: 'WATCH', countSuffix: '· 2' },
        isMine: true,
        selected: true,
      },
    ]);
  });

  it('marks selected:false for a non-matching selectedId, and isMine:false for a peer group', () => {
    const rows = buildGroupRows([group({ id: 'ct-active', owner: 'celestrak' })], 'leo-a');
    expect(rows[0]!.selected).toBe(false);
    expect(rows[0]!.isMine).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// formatUpdatedLabel
// ---------------------------------------------------------------------------

describe('formatUpdatedLabel', () => {
  it('returns a mock fixture literal string verbatim (not a valid Date)', () => {
    expect(formatUpdatedLabel('2m ago')).toBe('2m ago');
    expect(formatUpdatedLabel('just now')).toBe('just now');
  });

  it('renders "just now" for a timestamp under 45s old', () => {
    const now = new Date('2026-07-11T12:00:30Z');
    expect(formatUpdatedLabel('2026-07-11T12:00:00Z', now)).toBe('just now');
  });

  it('renders seconds for 45s–60s', () => {
    const now = new Date('2026-07-11T12:00:50Z');
    expect(formatUpdatedLabel('2026-07-11T12:00:00Z', now)).toBe('50s ago');
  });

  it('renders minutes for 1m–1h', () => {
    const now = new Date('2026-07-11T12:05:00Z');
    expect(formatUpdatedLabel('2026-07-11T12:00:00Z', now)).toBe('5m ago');
  });

  it('renders hours for 1h–24h', () => {
    const now = new Date('2026-07-11T15:00:00Z');
    expect(formatUpdatedLabel('2026-07-11T12:00:00Z', now)).toBe('3h ago');
  });

  it('renders days for 24h+', () => {
    const now = new Date('2026-07-14T12:00:00Z');
    expect(formatUpdatedLabel('2026-07-11T12:00:00Z', now)).toBe('3d ago');
  });
});

// ---------------------------------------------------------------------------
// formatEventTca / eventStateColor / buildConjunctionEvents
// ---------------------------------------------------------------------------

describe('formatEventTca', () => {
  it('ports the mock\'s slice(5,16).replace("T"," ") exactly', () => {
    expect(formatEventTca('2026-06-26T11:55:00Z')).toBe('06-26 11:55');
  });
});

describe('eventStateColor', () => {
  it('maps warn/review/other', () => {
    expect(eventStateColor('warn')).toBe('#ff6b6b');
    expect(eventStateColor('review')).toBe('#ffb24d');
    expect(eventStateColor('clear')).toBe('#cfe3ec');
  });
});

describe('buildConjunctionEvents', () => {
  it('clear → no events', () => {
    expect(buildConjunctionEvents('clear')).toEqual([]);
  });

  it('watch → 1 event', () => {
    expect(buildConjunctionEvents('watch')).toHaveLength(1);
    expect(buildConjunctionEvents('watch')[0]).toEqual({
      object: 'SAT-39210',
      tca: '06-26 11:55',
      missKm: '0.42',
      pc: '7.3e-4',
      stateColor: '#ff6b6b',
    });
  });

  it('critical → 2 events', () => {
    expect(buildConjunctionEvents('critical')).toHaveLength(2);
  });
});

// ---------------------------------------------------------------------------
// buildGroupConjunctionSection (CONJUNCTION MONITOR)
// ---------------------------------------------------------------------------

describe('buildGroupConjunctionSection', () => {
  it('demo-tags a fixture-sourced status and includes its events', () => {
    const section = buildGroupConjunctionSection({ conj: 'critical', conjN: 3 });
    expect(section.isDemo).toBe(true);
    expect(section.label).toBe('CRITICAL');
    expect(section.dotColor).toBe('#ff6b6b');
    expect(section.events).toHaveLength(2);
    expect(section.subText).toContain('maneuver options');
  });

  it('renders an honest no-data section for conj:"" — isDemo:false, no events', () => {
    const section = buildGroupConjunctionSection({ conj: '', conjN: 0 });
    expect(section).toEqual({
      isDemo: false,
      label: '—',
      dotColor: '#5a7a8a',
      subText: 'No conjunction screening data on this build — this group has no conjunction-engine surface yet.',
      events: [],
    });
  });
});

// ---------------------------------------------------------------------------
// groupOrbitalPath / GROUPS_CONJUNCTION_CONSOLE_PATH
// ---------------------------------------------------------------------------

describe('groupOrbitalPath', () => {
  it('builds /orbital?group={id}, URI-encoded', () => {
    expect(groupOrbitalPath('leo-a')).toBe('/orbital?group=leo-a');
    expect(groupOrbitalPath('a b')).toBe('/orbital?group=a%20b');
  });
});

describe('GROUPS_CONJUNCTION_CONSOLE_PATH', () => {
  it('is the console conjunction route', () => {
    expect(GROUPS_CONJUNCTION_CONSOLE_PATH).toBe('/console/conjunction');
  });
});

// ---------------------------------------------------------------------------
// buildGroupDetailView
// ---------------------------------------------------------------------------

describe('buildGroupDetailView', () => {
  it('a owner:self group: SCREEN button, no read-only note, deletable, not ownership-demo', () => {
    const view = buildGroupDetailView(group({ owner: 'self', updated: '2m ago' }));
    expect(view.isMine).toBe(true);
    expect(view.isPeer).toBe(false);
    expect(view.isOwnershipDemo).toBe(false);
    expect(view.deletable).toBe(true);
    expect(view.readOnlyNote).toBeNull();
    expect(view.kindLabel).toBe('MY GROUP · YOU ADMINISTER');
    expect(view.screenButton).toEqual({
      label: 'SCREEN FOR CONJUNCTIONS',
      glyph: '⊘',
      color: '#ffb3b3',
      border: 'rgba(255,107,107,0.5)',
      background: 'rgba(255,107,107,0.14)',
    });
    expect(view.updatedLabel).toBe('2m ago');
  });

  it('a peer/provider group: MONITOR button, read-only note, not deletable, ownership-demo', () => {
    const view = buildGroupDetailView(group({ owner: 'celestrak', ownerName: 'CelesTrak Provider' }));
    expect(view.isMine).toBe(false);
    expect(view.isPeer).toBe(true);
    expect(view.isOwnershipDemo).toBe(true);
    expect(view.deletable).toBe(false);
    expect(view.readOnlyNote).toBe(GROUPS_READ_ONLY_NOTE);
    expect(view.kindLabel).toBe('PEER GROUP · MONITOR ONLY');
    expect(view.screenButton.label).toBe('MONITOR CONJUNCTIONS');
    expect(view.screenButton.glyph).toBe('◉');
  });

  it('membersLabel uses the lowercase "obj" suffix (detail-grid convention)', () => {
    expect(buildGroupDetailView(group({ count: 9 })).membersLabel).toBe('9 obj');
  });

  it('openIn3dPath points at /orbital?group={id}', () => {
    expect(buildGroupDetailView(group({ id: 'iss-env' })).openIn3dPath).toBe('/orbital?group=iss-env');
  });

  it('conjunction section reflects the group\'s own conj/conjN', () => {
    const view = buildGroupDetailView(group({ conj: '', conjN: 0 }));
    expect(view.conjunction.isDemo).toBe(false);
    expect(view.conjunction.events).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// resolveSelectedGroup
// ---------------------------------------------------------------------------

describe('resolveSelectedGroup', () => {
  it('returns the group matching selectedId', () => {
    const groups = [group({ id: 'a' }), group({ id: 'b' })];
    expect(resolveSelectedGroup(groups, 'b')!.id).toBe('b');
  });

  it('falls back to the first group when selectedId does not match', () => {
    const groups = [group({ id: 'a' }), group({ id: 'b' })];
    expect(resolveSelectedGroup(groups, 'nope')!.id).toBe('a');
  });

  it('falls back to the first group when selectedId is null', () => {
    const groups = [group({ id: 'a' })];
    expect(resolveSelectedGroup(groups, null)!.id).toBe('a');
  });

  it('returns null for an empty list', () => {
    expect(resolveSelectedGroup([], 'a')).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// resolveDeepLinkGroupId
// ---------------------------------------------------------------------------

describe('resolveDeepLinkGroupId', () => {
  it('matches by exact id', () => {
    expect(resolveDeepLinkGroupId(SEED_GROUPS, 'iss-env')).toBe('iss-env');
  });

  it('matches by case-insensitive name', () => {
    expect(resolveDeepLinkGroupId(SEED_GROUPS, 'geo belt watch')).toBe('geo-watch');
    expect(resolveDeepLinkGroupId(SEED_GROUPS, 'GEO BELT WATCH')).toBe('geo-watch');
  });

  it('returns null for no match', () => {
    expect(resolveDeepLinkGroupId(SEED_GROUPS, 'does-not-exist')).toBeNull();
  });

  it('returns null for a blank or null param', () => {
    expect(resolveDeepLinkGroupId(SEED_GROUPS, '')).toBeNull();
    expect(resolveDeepLinkGroupId(SEED_GROUPS, '   ')).toBeNull();
    expect(resolveDeepLinkGroupId(SEED_GROUPS, null)).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// Demo tag title copy (non-empty, referential — exercised by the view)
// ---------------------------------------------------------------------------

describe('demo tag titles', () => {
  it('are non-empty, distinct strings', () => {
    expect(GROUPS_OWNERSHIP_DEMO_TAG_TITLE.length).toBeGreaterThan(0);
    expect(GROUPS_CONJUNCTION_DEMO_TAG_TITLE.length).toBeGreaterThan(0);
    expect(GROUPS_OWNERSHIP_DEMO_TAG_TITLE).not.toBe(GROUPS_CONJUNCTION_DEMO_TAG_TITLE);
  });
});

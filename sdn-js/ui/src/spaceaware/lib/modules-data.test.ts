import { describe, expect, it } from 'vitest';
import { SdnApiError, type RequestOptions } from '../../lib/auth/sdn-api-client';
import {
  MODULES_ANONYMOUS_NOTE,
  MODULES_EMPTY_LABEL,
  MODULES_LOADING_LABEL,
  MODULE_ACTION_FORBIDDEN_MESSAGE,
  MODULE_ACTION_UNAUTHENTICATED_MESSAGE,
  MODULE_LOAD_UNSUPPORTED_TOOLTIP,
  MODULE_SUBSCRIBE_HREF,
  MODULE_SUBSCRIBE_TOOLTIP,
  buildInstalledModuleCard,
  buildLockedModuleCard,
  buildModuleCards,
  loadModulesDashboardData,
  moduleCardActionStyle,
  modulesEmptyStateLabel,
  parseRuntimeModuleEntry,
  parseRuntimeSnapshot,
  parseStorefrontModuleListings,
  runModuleAction,
  type ModulesApiClient,
  type RuntimeModuleEntry,
  type StorefrontModuleListing,
} from './modules-data';

// ---------------------------------------------------------------------------
// parseRuntimeModuleEntry / parseRuntimeSnapshot
// ---------------------------------------------------------------------------

describe('parseRuntimeModuleEntry', () => {
  it('parses the real running spaceaware-ai-log shape', () => {
    const entry = parseRuntimeModuleEntry({
      id: 'spaceaware-ai-log',
      version: '1.0.0',
      status: 'running',
      ui: { title: 'SpaceAware AI Log', description: 'x', icon: '🛰', color: '#111', textColor: '#eee', url: '/ui/ai-log' },
      manifest: { pluginId: 'spaceaware-ai-log', pluginFamily: 'analysis' },
      actions: [
        { actionId: 'load', label: 'Load', description: 'Load this module artifact without starting it.', enabled: false },
        { actionId: 'unload', label: 'Unload', description: 'Unload this module artifact from the runtime.', enabled: true, destructive: true },
      ],
    });
    expect(entry).toEqual({
      id: 'spaceaware-ai-log',
      version: '1.0.0',
      status: 'running',
      ui: { title: 'SpaceAware AI Log', description: 'x', icon: '🛰', color: '#111', textColor: '#eee', url: '/ui/ai-log' },
      pluginFamily: 'analysis',
      actions: [
        { actionId: 'load', label: 'Load', description: 'Load this module artifact without starting it.', enabled: false },
        { actionId: 'unload', label: 'Unload', description: 'Unload this module artifact from the runtime.', enabled: true },
      ],
    });
  });

  it('returns null for an entry with no id', () => {
    expect(parseRuntimeModuleEntry({ status: 'running' })).toBeNull();
  });

  it('returns null for a non-object payload', () => {
    expect(parseRuntimeModuleEntry(null)).toBeNull();
    expect(parseRuntimeModuleEntry('x')).toBeNull();
  });

  it('degrades ui/manifest/actions honestly when absent', () => {
    const entry = parseRuntimeModuleEntry({ id: 'bare-module', status: 'registered' });
    expect(entry?.ui).toBeNull();
    expect(entry?.pluginFamily).toBeNull();
    expect(entry?.actions).toEqual([]);
    expect(entry?.version).toBeNull();
  });

  it('drops action entries with no actionId', () => {
    const entry = parseRuntimeModuleEntry({ id: 'm', status: 'running', actions: [{ label: 'no id' }, { actionId: 'unload', label: 'Unload', enabled: true }] });
    expect(entry?.actions).toEqual([{ actionId: 'unload', label: 'Unload', description: '', enabled: true }]);
  });
});

describe('parseRuntimeSnapshot', () => {
  it('parses {generatedAt,count,modules}', () => {
    const snapshot = parseRuntimeSnapshot({
      generatedAt: '2026-07-11T00:00:00Z',
      count: 1,
      modules: [{ id: 'spaceaware-ai-log', status: 'running' }],
    });
    expect(snapshot.generatedAt).toBe('2026-07-11T00:00:00Z');
    expect(snapshot.count).toBe(1);
    expect(snapshot.modules).toHaveLength(1);
  });

  it('degrades to an empty snapshot for a malformed payload', () => {
    expect(parseRuntimeSnapshot(null)).toEqual({ generatedAt: null, count: 0, modules: [] });
  });

  it('drops malformed module entries without crashing the whole snapshot', () => {
    const snapshot = parseRuntimeSnapshot({ modules: [{ id: 'ok', status: 'running' }, { status: 'no id' }, null] });
    expect(snapshot.modules).toHaveLength(1);
    expect(snapshot.count).toBe(1);
  });
});

// ---------------------------------------------------------------------------
// parseStorefrontModuleListings
// ---------------------------------------------------------------------------

describe('parseStorefrontModuleListings', () => {
  it('parses a real wasm_module listing', () => {
    const listings = parseStorefrontModuleListings({
      listings: [
        {
          listing_id: 'lst-1',
          listing_kind: 'wasm_module',
          title: 'Orbit Determination',
          protected_delivery: { module_id: 'od-module' },
          pricing: [{ name: 'monthly', price_amount: 4900, price_currency: 'usd' }],
        },
      ],
      total: 1,
    });
    expect(listings).toEqual([{ listingId: 'lst-1', title: 'Orbit Determination', moduleId: 'od-module', priceAmount: 4900, priceCurrency: 'usd' }]);
  });

  it('handles listings:null honestly — matches this node\'s real response', () => {
    expect(parseStorefrontModuleListings({ listings: null, total: 0, facets: {} })).toEqual([]);
  });

  it('drops a listing with no protected_delivery.module_id (a plain data listing, not a module)', () => {
    const listings = parseStorefrontModuleListings({
      listings: [{ listing_id: 'lst-2', title: 'CAT bulk feed', protected_delivery: { module_id: '' }, pricing: [] }],
    });
    expect(listings).toEqual([]);
  });

  it('renders priceAmount as null (not 0) when the listing has no pricing tier', () => {
    const listings = parseStorefrontModuleListings({
      listings: [{ listing_id: 'lst-3', title: 'Free Module', protected_delivery: { module_id: 'free-mod' }, pricing: [] }],
    });
    expect(listings[0]?.priceAmount).toBeNull();
  });

  it('degrades to [] for a non-object payload', () => {
    expect(parseStorefrontModuleListings(null)).toEqual([]);
    expect(parseStorefrontModuleListings(undefined)).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// buildInstalledModuleCard
// ---------------------------------------------------------------------------

describe('buildInstalledModuleCard', () => {
  const runningEntry: RuntimeModuleEntry = {
    id: 'spaceaware-ai-log',
    version: '1.0.0',
    status: 'running',
    ui: { title: 'SpaceAware AI Log', description: null, icon: null, color: null, textColor: null, url: null },
    pluginFamily: 'analysis',
    actions: [
      { actionId: 'load', label: 'Load', description: 'Load this module artifact without starting it.', enabled: false },
      { actionId: 'unload', label: 'Unload', description: 'Unload this module artifact from the runtime.', enabled: true },
    ],
  };

  it('maps a running module to a LOADED/UNLOAD card', () => {
    const card = buildInstalledModuleCard(runningEntry, null);
    expect(card.statusLabel).toBe('LOADED');
    expect(card.statusColor).toBe('#5ad6a0');
    expect(card.actionLabel).toBe('UNLOAD');
    expect(card.actionKind).toBe('unload');
    expect(card.actionEnabled).toBe(true);
    expect(card.actionTooltip).toBe('Unload this module artifact from the runtime.');
  });

  it('falls back to an enabled START verb when the server gates load off (module without RuntimeModuleLoader)', () => {
    // Live shape from this build: unloaded spaceaware-ai-log advertises
    // load:disabled (no RuntimeModuleLoader) but start:enabled — the card
    // must surface the verb that will actually run, labeled honestly.
    const entry: RuntimeModuleEntry = {
      ...runningEntry,
      status: 'unloaded',
      actions: [
        { actionId: 'load', label: 'Load', description: 'Load this module artifact without starting it.', enabled: false },
        { actionId: 'start', label: 'Start', description: 'Start or resume this module runtime.', enabled: true },
      ],
    };
    const card = buildInstalledModuleCard(entry, null);
    expect(card.statusLabel).toBe('UNLOADED');
    expect(card.actionLabel).toBe('START');
    expect(card.actionKind).toBe('start');
    expect(card.actionEnabled).toBe(true);
    expect(card.actionTooltip).toBe('Start or resume this module runtime.');
  });

  it('maps a non-running module to its real status uppercased and a LOAD action', () => {
    const stopped: RuntimeModuleEntry = { ...runningEntry, status: 'stopped', actions: [
      { actionId: 'load', label: 'Load', description: 'Load this module artifact without starting it.', enabled: true },
      { actionId: 'unload', label: 'Unload', description: 'x', enabled: false },
    ] };
    const card = buildInstalledModuleCard(stopped, null);
    expect(card.statusLabel).toBe('STOPPED');
    expect(card.actionLabel).toBe('LOAD');
    expect(card.actionKind).toBe('load');
    expect(card.actionEnabled).toBe(true);
  });

  it('never renders LOCKED for an installed module even with no listing', () => {
    const card = buildInstalledModuleCard({ ...runningEntry, status: 'paused' }, null);
    expect(card.statusLabel).not.toBe('LOCKED');
  });

  it('colors an error status distinctly', () => {
    const card = buildInstalledModuleCard({ ...runningEntry, status: 'error' }, null);
    expect(card.statusLabel).toBe('ERROR');
    expect(card.statusColor).toBe('#ff6b6b');
  });

  it('falls back the action button to disabled with an honest tooltip when the server reports no matching action', () => {
    const card = buildInstalledModuleCard({ ...runningEntry, actions: [] }, null);
    expect(card.actionEnabled).toBe(false);
    expect(card.actionKind).toBeNull();
    expect(card.actionTooltip).toBe(MODULE_LOAD_UNSUPPORTED_TOOLTIP);
  });

  it('falls back the display name to the module id when ui.title is absent', () => {
    const card = buildInstalledModuleCard({ ...runningEntry, ui: null }, null);
    expect(card.name).toBe('spaceaware-ai-log');
  });

  it('renders the honest "id · vVersion" provider line, never a fabricated provider name', () => {
    const card = buildInstalledModuleCard(runningEntry, null);
    expect(card.providerLine).toBe('spaceaware-ai-log · v1.0.0');
  });

  it('omits the version suffix when the module reports no version', () => {
    const card = buildInstalledModuleCard({ ...runningEntry, version: null }, null);
    expect(card.providerLine).toBe('spaceaware-ai-log');
  });

  it('carries the real manifest.pluginFamily as the category label, or null when absent — never a fabricated PROPAGATION/INGEST/ANALYSIS label', () => {
    expect(buildInstalledModuleCard(runningEntry, null).categoryLabel).toBe('analysis');
    expect(buildInstalledModuleCard({ ...runningEntry, pluginFamily: null }, null).categoryLabel).toBeNull();
  });

  it('is not PAID when no matching listing exists', () => {
    expect(buildInstalledModuleCard(runningEntry, null).paid).toBe(false);
  });

  it('is PAID when a matching real listing has a positive price', () => {
    const listing: StorefrontModuleListing = { listingId: 'l1', title: 'x', moduleId: 'spaceaware-ai-log', priceAmount: 999, priceCurrency: 'usd' };
    expect(buildInstalledModuleCard(runningEntry, listing).paid).toBe(true);
  });

  it('is not PAID when the matched listing has no price (priceAmount null)', () => {
    const listing: StorefrontModuleListing = { listingId: 'l1', title: 'x', moduleId: 'spaceaware-ai-log', priceAmount: null, priceCurrency: null };
    expect(buildInstalledModuleCard(runningEntry, listing).paid).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// buildLockedModuleCard
// ---------------------------------------------------------------------------

describe('buildLockedModuleCard', () => {
  const listing: StorefrontModuleListing = { listingId: 'lst-1', title: 'Orbit Determination', moduleId: 'od-module', priceAmount: 4900, priceCurrency: 'usd' };

  it('renders a LOCKED/SUBSCRIBE card from a synthetic marketplace listing', () => {
    const card = buildLockedModuleCard(listing);
    expect(card).toEqual({
      id: 'od-module',
      name: 'Orbit Determination',
      paid: true,
      categoryLabel: null,
      providerLine: 'od-module',
      statusLabel: 'LOCKED',
      statusColor: '#ffb24d',
      actionLabel: 'SUBSCRIBE',
      actionEnabled: true,
      actionTooltip: MODULE_SUBSCRIBE_TOOLTIP,
      actionKind: 'subscribe',
      subscribeHref: MODULE_SUBSCRIBE_HREF,
    });
  });

  it('falls back the name to the module id when the listing has no title', () => {
    const card = buildLockedModuleCard({ ...listing, title: '' });
    expect(card.name).toBe('od-module');
  });
});

// ---------------------------------------------------------------------------
// moduleCardActionStyle
// ---------------------------------------------------------------------------

describe('moduleCardActionStyle', () => {
  it('styles a subscribe action amber (locked)', () => {
    const style = moduleCardActionStyle('subscribe');
    expect(style.color).toBe('#ffb24d');
  });

  it('styles load/unload/null ice-blue', () => {
    expect(moduleCardActionStyle('load').color).toBe('#9fd4f5');
    expect(moduleCardActionStyle('unload').color).toBe('#9fd4f5');
    expect(moduleCardActionStyle(null).color).toBe('#9fd4f5');
  });
});

// ---------------------------------------------------------------------------
// buildModuleCards — merge
// ---------------------------------------------------------------------------

describe('buildModuleCards', () => {
  const installed: RuntimeModuleEntry = {
    id: 'spaceaware-ai-log',
    version: '1.0.0',
    status: 'running',
    ui: null,
    pluginFamily: null,
    actions: [{ actionId: 'unload', label: 'Unload', description: 'x', enabled: true }],
  };

  it('returns [] for empty everything (real empty-catalog state on this build)', () => {
    expect(buildModuleCards([], [])).toEqual([]);
  });

  it('produces one card per installed module when no listings exist', () => {
    const cards = buildModuleCards([installed], []);
    expect(cards).toHaveLength(1);
    expect(cards[0]?.id).toBe('spaceaware-ai-log');
    expect(cards[0]?.paid).toBe(false);
  });

  it('marks an installed module PAID when a matching listing exists, without duplicating it as a LOCKED card', () => {
    const listing: StorefrontModuleListing = { listingId: 'l1', title: 'x', moduleId: 'spaceaware-ai-log', priceAmount: 500, priceCurrency: 'usd' };
    const cards = buildModuleCards([installed], [listing]);
    expect(cards).toHaveLength(1);
    expect(cards[0]?.paid).toBe(true);
    expect(cards[0]?.statusLabel).toBe('LOADED');
  });

  it('adds a LOCKED card for a marketplace listing not yet installed (synthetic fixture — none exist today)', () => {
    const listing: StorefrontModuleListing = { listingId: 'l2', title: 'Conjunction Screening', moduleId: 'ca-screen', priceAmount: 2900, priceCurrency: 'usd' };
    const cards = buildModuleCards([installed], [listing]);
    expect(cards).toHaveLength(2);
    const locked = cards.find((c) => c.id === 'ca-screen');
    expect(locked?.statusLabel).toBe('LOCKED');
    expect(locked?.actionKind).toBe('subscribe');
  });
});

// ---------------------------------------------------------------------------
// Empty state
// ---------------------------------------------------------------------------

describe('modulesEmptyStateLabel', () => {
  it('shows a loading label before data arrives', () => {
    expect(modulesEmptyStateLabel(false, 0)).toBe(MODULES_LOADING_LABEL);
  });

  it('shows the honest "empty catalog" panel for zero cards', () => {
    expect(modulesEmptyStateLabel(true, 0)).toBe(MODULES_EMPTY_LABEL);
  });

  it('shows "" once cards exist', () => {
    expect(modulesEmptyStateLabel(true, 2)).toBe('');
  });

  it('MODULES_ANONYMOUS_NOTE is a distinct, honest string', () => {
    expect(MODULES_ANONYMOUS_NOTE.toLowerCase()).toContain('sign in');
  });
});

// ---------------------------------------------------------------------------
// loadModulesDashboardData — fetch orchestration
// ---------------------------------------------------------------------------

describe('loadModulesDashboardData', () => {
  function fakeApiClient(handlers: Record<string, unknown>): ModulesApiClient {
    return {
      requestJson: async <T,>(path: string) => {
        if (!(path in handlers)) throw new Error(`unexpected path ${path}`);
        const value = handlers[path];
        if (value instanceof Error) throw value;
        return { status: 200, data: value as T, etag: null, notModified: false };
      },
    } as unknown as ModulesApiClient;
  }

  it('parses a successful pair of responses', async () => {
    const apiClient = fakeApiClient({
      '/modules/runtime': { generatedAt: 't', count: 1, modules: [{ id: 'm1', status: 'running' }] },
      '/api/storefront/listings/search': { listings: null, total: 0, facets: {} },
    });
    const data = await loadModulesDashboardData(apiClient);
    expect(data.runtime?.modules).toHaveLength(1);
    expect(data.listings).toEqual([]);
    expect(data.runtimeUnauthorized).toBe(false);
  });

  it('marks runtimeUnauthorized on a real 401, distinct from other failures', async () => {
    const apiClient: ModulesApiClient = {
      requestJson: async <T,>(path: string) => {
        if (path === '/modules/runtime') throw new SdnApiError(401, { code: 'unauthorized', message: 'x' }, path);
        return { status: 200, data: { listings: null } as T, etag: null, notModified: false };
      },
    };
    const data = await loadModulesDashboardData(apiClient);
    expect(data.runtime).toBeNull();
    expect(data.runtimeUnauthorized).toBe(true);
  });

  it('never rejects — every endpoint failing resolves to an honest empty snapshot', async () => {
    const apiClient = fakeApiClient({
      '/modules/runtime': new Error('offline'),
      '/api/storefront/listings/search': new Error('offline'),
    });
    const data = await loadModulesDashboardData(apiClient);
    expect(data).toEqual({ runtime: null, listings: [], runtimeUnauthorized: false });
  });

  it('posts an empty body to the listings search endpoint at the root base', async () => {
    let capturedOpts: RequestOptions | undefined;
    const apiClient: ModulesApiClient = {
      requestJson: async <T,>(path: string, opts?: RequestOptions) => {
        if (path === '/api/storefront/listings/search') capturedOpts = opts;
        return { status: 200, data: (path === '/modules/runtime' ? { modules: [] } : { listings: null }) as T, etag: null, notModified: false };
      },
    };
    await loadModulesDashboardData(apiClient);
    expect(capturedOpts).toEqual({ method: 'POST', base: 'root', body: {} });
  });
});

// ---------------------------------------------------------------------------
// runModuleAction — mutation
// ---------------------------------------------------------------------------

describe('runModuleAction', () => {
  function fakeClient(fn: (path: string, opts?: RequestOptions) => unknown): ModulesApiClient {
    return {
      requestJson: async <T,>(path: string, opts?: RequestOptions) => {
        const value = fn(path, opts);
        if (value instanceof Error) throw value;
        return { status: 200, data: value as T, etag: null, notModified: false };
      },
    } as unknown as ModulesApiClient;
  }

  it('succeeds on the real {ok:true,...} response', async () => {
    const client = fakeClient(() => ({ ok: true, moduleId: 'spaceaware-ai-log', actionId: 'unload' }));
    const result = await runModuleAction(client, 'spaceaware-ai-log', 'unload');
    expect(result).toEqual({ ok: true, message: null });
  });

  it('hits the exact /modules/runtime/{id}/actions/{action} path', async () => {
    let capturedPath = '';
    const client: ModulesApiClient = {
      requestJson: async <T,>(path: string) => {
        capturedPath = path;
        return { status: 200, data: { ok: true } as T, etag: null, notModified: false };
      },
    };
    await runModuleAction(client, 'spaceaware-ai-log', 'unload');
    expect(capturedPath).toBe('/modules/runtime/spaceaware-ai-log/actions/unload');
  });

  it('URL-encodes the module id and action id', async () => {
    let capturedPath = '';
    const client: ModulesApiClient = {
      requestJson: async <T,>(path: string) => {
        capturedPath = path;
        return { status: 200, data: { ok: true } as T, etag: null, notModified: false };
      },
    };
    await runModuleAction(client, 'weird/id x', 'un load');
    expect(capturedPath).toBe('/modules/runtime/weird%2Fid%20x/actions/un%20load');
  });

  it('treats a 200 without ok:true as a soft failure', async () => {
    const client = fakeClient(() => ({ ok: false }));
    const result = await runModuleAction(client, 'm', 'load');
    expect(result.ok).toBe(false);
    expect(result.message).toBeTruthy();
  });

  it('gives an honest message for 401', async () => {
    const client = fakeClient(() => new SdnApiError(401, { code: 'unauthorized', message: 'x' }, '/modules/runtime/m/actions/load'));
    const result = await runModuleAction(client, 'm', 'load');
    expect(result).toEqual({ ok: false, message: MODULE_ACTION_UNAUTHENTICATED_MESSAGE });
  });

  it('gives an honest message for 403 (below admin trust)', async () => {
    const client = fakeClient(() => new SdnApiError(403, { code: 'forbidden', message: 'x' }, '/modules/runtime/m/actions/load'));
    const result = await runModuleAction(client, 'm', 'load');
    expect(result).toEqual({ ok: false, message: MODULE_ACTION_FORBIDDEN_MESSAGE });
  });

  it('falls back to an HTTP-status message for a plain-text error body (real server behavior — http.Error is not JSON)', async () => {
    const client = fakeClient(() => new SdnApiError(400, null, '/modules/runtime/m/actions/load'));
    const result = await runModuleAction(client, 'm', 'load');
    expect(result.ok).toBe(false);
    expect(result.message).toContain('400');
  });

  it('handles a generic network throw honestly', async () => {
    const client = fakeClient(() => new TypeError('Failed to fetch'));
    const result = await runModuleAction(client, 'm', 'load');
    expect(result).toEqual({ ok: false, message: 'Action failed — network error.' });
  });
});

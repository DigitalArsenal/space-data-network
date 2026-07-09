import { afterEach, describe, expect, it, vi } from 'vitest';

import { createInitialEcosystemState, runEcosystemAction, selectEcosystemItem } from './model';
import { renderNetworkEcosystemDemo } from './view';

afterEach(() => {
  vi.doUnmock('./dom');
  vi.resetModules();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('network ecosystem demo view', () => {
  it('renders the shape legend, controls, graph, details, and event log', () => {
    const html = renderNetworkEcosystemDemo(createInitialEcosystemState());

    expect(html).toContain('data-ecosystem-graph');
    expect(html).toContain('Sandbox mode');
    expect(html).toContain('Live mode');
    expect(html).toContain('Circle');
    expect(html).toContain('Triangle');
    expect(html).toContain('Square');
    expect(html).toContain('Diamond');
    expect(html).toContain('Create test OMM');
    expect(html).toContain('Module name');
    expect(html).toContain('Create module');
    expect(html).toContain('Run module');
    expect(html).toContain('data-action="create-test-data"');
    expect(html).toContain('data-action="create-module-listing"');
    expect(html).toContain('data-action="simulate-module-invocation"');
  });

  it('renders selected item detail and event evidence', () => {
    let state = createInitialEcosystemState();
    state = selectEcosystemItem(state, 'module-sgp4');
    state = runEcosystemAction(state, { type: 'pin-product', targetId: 'data-dpm' });

    const html = renderNetworkEcosystemDemo(state);

    expect(html).toContain('SGP4 Propagator Module');
    expect(html).toContain('Sandbox pin recorded');
    expect(html).toContain('data-item-id="module-sgp4"');
  });

  it('escapes adversarial dynamic text in details, module input, and event log', () => {
    const adversarialText = `<orbit>&"payload'`;
    const state = {
      ...createInitialEcosystemState(),
      selectedItemId: 'module-sgp4',
      items: createInitialEcosystemState().items.map((item) =>
        item.id === 'module-sgp4'
          ? {
              ...item,
              title: adversarialText,
              description: `Selected ${adversarialText}`,
            }
          : item,
      ),
      events: [
        {
          type: 'module-listed' as const,
          title: `Event ${adversarialText}`,
          detail: `Detail ${adversarialText}`,
        },
      ],
      moduleListings: [
        {
          name: `Module ${adversarialText}`,
          moduleId: 'demo.adversarial',
          inputSchema: 'OMM',
          outputSchema: 'OEM',
          signed: true,
        },
      ],
    };

    const html = renderNetworkEcosystemDemo(state);

    expect(html).toContain('&lt;orbit&gt;&amp;&quot;payload&#39;');
    expect(html).toContain('value="Module &lt;orbit&gt;&amp;&quot;payload&#39;"');
    expect(html).not.toContain(adversarialText);
  });
});

describe('network ecosystem demo entrypoint', () => {
  it('mounts on DOMContentLoaded when the root appears after module evaluation', async () => {
    vi.resetModules();
    const mountNetworkEcosystemDemo = vi.fn();
    vi.doMock('./dom', () => ({ mountNetworkEcosystemDemo }));

    const root = { nodeType: 1 } as HTMLElement;
    let domContentLoadedListener: (() => void) | undefined;
    let queryCount = 0;
    const documentStub = {
      readyState: 'loading',
      querySelector: vi.fn((): HTMLElement | null => {
        queryCount += 1;
        return queryCount === 1 ? null : root;
      }),
      addEventListener: vi.fn(
        (eventName: string, listener: () => void, options?: AddEventListenerOptions): void => {
          expect(options).toEqual({ once: true });
          if (eventName === 'DOMContentLoaded') {
            domContentLoadedListener = listener;
          }
        },
      ),
    };
    vi.stubGlobal('document', documentStub);

    await import('./index');

    expect(mountNetworkEcosystemDemo).not.toHaveBeenCalled();
    expect(documentStub.addEventListener).toHaveBeenCalledWith(
      'DOMContentLoaded',
      expect.any(Function),
      { once: true },
    );

    domContentLoadedListener?.();

    expect(mountNetworkEcosystemDemo).toHaveBeenCalledTimes(1);
    expect(mountNetworkEcosystemDemo).toHaveBeenCalledWith(root);
  });
});

import { describe, expect, it } from 'vitest';

import {
  createInitialEcosystemState,
  ecosystemShapeLegend,
  runEcosystemAction,
  selectEcosystemItem,
} from './model';

describe('network ecosystem demo model', () => {
  it('starts in sandbox mode with distinct nodes, data, modules, and verification artifacts', () => {
    const state = createInitialEcosystemState();

    expect(state.mode).toBe('sandbox');
    expect(state.items.some((item) => item.id === 'node-celestrak' && item.kind === 'node')).toBe(true);
    expect(state.items.some((item) => item.id === 'data-omm' && item.kind === 'data')).toBe(true);
    expect(state.items.some((item) => item.id === 'module-sgp4' && item.kind === 'module')).toBe(true);
    expect(state.items.some((item) => item.id === 'badge-plg' && item.kind === 'evidence')).toBe(true);
    expect(ecosystemShapeLegend.map((entry) => entry.shape)).toEqual([
      'circle',
      'triangle',
      'square',
      'diamond',
    ]);
  });

  it('records scenario actions and marks live mode as explicit and disconnected', () => {
    let state = createInitialEcosystemState();

    state = runEcosystemAction(state, { type: 'create-test-data', schema: 'OMM' });
    state = runEcosystemAction(state, { type: 'subscribe-channel', sourceId: 'celestrak-eth', standardCode: 'OMM' });
    state = runEcosystemAction(state, { type: 'pin-product', targetId: 'data-dpm' });
    state = runEcosystemAction(state, {
      type: 'create-module-listing',
      name: 'Demo SGP4',
      moduleId: 'demo.sgp4',
      inputSchema: 'OMM',
      outputSchema: 'OEM',
    });
    state = runEcosystemAction(state, { type: 'simulate-module-invocation', moduleId: 'demo.sgp4' });
    state = runEcosystemAction(state, { type: 'set-mode', mode: 'live' });

    expect(state.mode).toBe('live');
    expect(state.live.explicitlyRequested).toBe(true);
    expect(state.live.connections.every((connection) => connection.status === 'unavailable')).toBe(true);
    expect(state.moduleListings).toEqual([
      {
        name: 'Demo SGP4',
        moduleId: 'demo.sgp4',
        inputSchema: 'OMM',
        outputSchema: 'OEM',
        signed: true,
      },
    ]);
    expect(state.invocations).toEqual([
      {
        moduleId: 'demo.sgp4',
        inputSchema: 'OMM',
        outputSchema: 'OEM',
        status: 'simulated',
      },
    ]);
    expect(state.events.map((event) => event.type)).toEqual([
      'data-created',
      'channel-subscribed',
      'artifact-pinned',
      'module-listed',
      'module-invoked',
      'live-mode-requested',
    ]);
  });

  it('selects items without mutating graph state', () => {
    const state = createInitialEcosystemState();
    const selected = selectEcosystemItem(state, 'module-sgp4');

    expect(selected.selectedItemId).toBe('module-sgp4');
    expect(state.selectedItemId).toBe('node-browser');
    expect(selected.items).toEqual(state.items);
  });
});

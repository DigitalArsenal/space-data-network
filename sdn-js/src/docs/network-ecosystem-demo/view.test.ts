import { describe, expect, it } from 'vitest';

import { createInitialEcosystemState, runEcosystemAction, selectEcosystemItem } from './model';
import { renderNetworkEcosystemDemo } from './view';

describe('network ecosystem demo view', () => {
  it('renders the shape legend, controls, graph, details, and event log', () => {
    const html = renderNetworkEcosystemDemo(createInitialEcosystemState());

    expect(html).toContain('data-ecosystem-graph');
    expect(html).toContain('Sandbox mode');
    expect(html).toContain('Live mode');
    expect(html).toContain('Circle');
    expect(html).toContain('Triangle');
    expect(html).toContain('Square');
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
});

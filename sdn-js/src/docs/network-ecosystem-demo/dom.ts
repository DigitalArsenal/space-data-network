import {
  buildSandboxChannelEvidence,
  buildSandboxPnmEvidence,
  createSandboxArtifactEvidence,
  requestLiveConnections,
} from './evidence';
import {
  createInitialEcosystemState,
  runEcosystemAction,
  selectEcosystemItem,
  type EcosystemState,
} from './model';
import { renderNetworkEcosystemDemo } from './view';

export interface NetworkEcosystemMount {
  root: HTMLElement;
  getState: () => EcosystemState;
  destroy: () => void;
}

const defaultModuleName = 'Demo SGP4';

export function mountNetworkEcosystemDemo(root: HTMLElement): NetworkEcosystemMount {
  let state = createInitialEcosystemState();
  const abortController = new AbortController();

  function render(): void {
    root.innerHTML = renderNetworkEcosystemDemo(state);
  }

  async function handleClick(event: MouseEvent): Promise<void> {
    const target = event.target;
    if (!(target instanceof Element)) {
      return;
    }

    const actionElement = target.closest<HTMLElement>('[data-action]');
    if (actionElement && root.contains(actionElement)) {
      event.preventDefault();
      await dispatchAction(actionElement.dataset.action ?? '');
      render();
      return;
    }

    if (selectItemFromTarget(target)) {
      render();
    }
  }

  function handleKeydown(event: KeyboardEvent): void {
    if (event.key !== 'Enter' && event.key !== ' ') {
      return;
    }

    if (selectItemFromTarget(event.target)) {
      event.preventDefault();
      render();
    }
  }

  function selectItemFromTarget(target: EventTarget | null): boolean {
    if (!(target instanceof Element)) {
      return false;
    }

    const itemElement = target.closest<HTMLElement>('[data-item-id]');
    if (!itemElement || !root.contains(itemElement)) {
      return false;
    }

    const itemId = itemElement.dataset.itemId;
    if (!itemId) {
      return false;
    }

    state = selectEcosystemItem(state, itemId);
    return true;
  }

  async function dispatchAction(action: string): Promise<void> {
    if (action === 'create-test-data') {
      const artifact = await createSandboxArtifactEvidence({
        schema: 'OMM',
        title: 'Demo OMM',
        payload: {
          objectId: '25544',
          epoch: '2026-07-08T00:00:00.000Z',
          meanMotion: 15.49,
        },
      });
      await buildSandboxPnmEvidence(artifact);
      state = runEcosystemAction(state, { type: 'create-test-data', schema: 'OMM' });
      return;
    }

    if (action === 'subscribe-channel') {
      const channel = buildSandboxChannelEvidence({ sourceId: 'celestrak-eth', standardCode: 'OMM' });
      state = runEcosystemAction(state, {
        type: 'subscribe-channel',
        sourceId: channel.sourceId,
        standardCode: channel.standardCode,
      });
      return;
    }

    if (action === 'pin-product') {
      state = runEcosystemAction(state, { type: 'pin-product', targetId: 'data-dpm' });
      return;
    }

    if (action === 'create-module-listing') {
      const name = readModuleName(root);
      state = runEcosystemAction(state, {
        type: 'create-module-listing',
        name,
        moduleId: moduleIdForName(name),
        inputSchema: 'OMM',
        outputSchema: 'OEM',
      });
      return;
    }

    if (action === 'simulate-module-invocation') {
      state = runEcosystemAction(state, {
        type: 'simulate-module-invocation',
        moduleId: state.moduleListings.at(-1)?.moduleId ?? 'demo.sgp4',
      });
      return;
    }

    if (action === 'set-live') {
      await requestLiveConnections();
      state = runEcosystemAction(state, { type: 'set-mode', mode: 'live' });
      return;
    }

    if (action === 'set-sandbox') {
      state = runEcosystemAction(state, { type: 'set-mode', mode: 'sandbox' });
    }
  }

  root.addEventListener('click', handleClick, { signal: abortController.signal });
  root.addEventListener('keydown', handleKeydown, { signal: abortController.signal });
  render();

  return {
    root,
    getState: () => state,
    destroy: () => {
      abortController.abort();
    },
  };
}

function readModuleName(root: HTMLElement): string {
  const name = root.querySelector<HTMLInputElement>('[data-module-name]')?.value.trim();
  return name && name.length > 0 ? name : defaultModuleName;
}

function moduleIdForName(name: string): string {
  const slug = name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .replace(/^demo-/, '');

  return `demo.${slug || 'sgp4'}`;
}

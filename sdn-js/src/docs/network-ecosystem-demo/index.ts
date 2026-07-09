import { mountNetworkEcosystemDemo } from './dom';

export { mountNetworkEcosystemDemo } from './dom';
export * from './model';
export * from './evidence';

let mounted = false;

function mountAvailableDemo(): boolean {
  if (mounted || typeof document === 'undefined') {
    return mounted;
  }

  const root = document.querySelector<HTMLElement>('[data-sdn-network-ecosystem-demo]');
  if (!root) {
    return false;
  }

  mountNetworkEcosystemDemo(root);
  mounted = true;
  return true;
}

if (!mountAvailableDemo() && typeof document !== 'undefined' && document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded', mountAvailableDemo, { once: true });
}

import { mountNetworkEcosystemDemo } from './dom';

export { mountNetworkEcosystemDemo } from './dom';
export * from './model';
export * from './evidence';

const root = typeof document !== 'undefined'
  ? document.querySelector<HTMLElement>('[data-sdn-network-ecosystem-demo]')
  : null;

if (root) {
  mountNetworkEcosystemDemo(root);
}

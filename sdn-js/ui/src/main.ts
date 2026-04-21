import { renderUpstreamWebUiBaseline } from './upstream-webui/index.js';

renderUpstreamWebUiBaseline().catch((error) => {
  const root = document.querySelector('#root');
  if (root instanceof HTMLElement) {
    root.textContent = String(error);
  }
  console.error('[sdn-dashboard] upstream webui cutover failed', error);
});

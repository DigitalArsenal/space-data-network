import { escapeHtml } from './dom/escape';
import { bootstrapAdminApp } from './bootstrap';
import './styles.css';

bootstrapAdminApp().catch((error) => {
  const root = document.querySelector('#app');
  if (root instanceof HTMLElement) {
    root.innerHTML = `<pre class="sdn-error">${escapeHtml(String(error))}</pre>`;
  }
  console.error('[sdn-ui] bootstrap failed', error);
});

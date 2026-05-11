const COI_SERVICE_WORKER_PATH = 'coi-serviceworker.js';
const COI_RELOAD_KEY = 'sdn:coi-serviceworker-reload';

export async function ensureCrossOriginIsolation(): Promise<void> {
  if (typeof window === 'undefined' || typeof navigator === 'undefined') return;
  if (globalThis.crossOriginIsolated) return;
  if (!('serviceWorker' in navigator)) return;
  if (!window.isSecureContext) return;

  try {
    const workerUrl = new URL(COI_SERVICE_WORKER_PATH, document.baseURI);
    await navigator.serviceWorker.register(workerUrl, { scope: new URL('./', document.baseURI).pathname });
    await navigator.serviceWorker.ready;
    if (!navigator.serviceWorker.controller && !window.sessionStorage.getItem(COI_RELOAD_KEY)) {
      window.sessionStorage.setItem(COI_RELOAD_KEY, '1');
      window.location.reload();
      return;
    }
    window.sessionStorage.removeItem(COI_RELOAD_KEY);
  } catch {
    window.sessionStorage.removeItem(COI_RELOAD_KEY);
  }
}

/*! Based on coi-serviceworker v0.1.7 - Guido Zuidhof and contributors, licensed under MIT */
// Cross-origin isolation for the whitepaper pages, plus a same-origin route to
// the interactive models. GitHub Pages cannot send COOP/COEP, so this worker adds
// them. The models live on the orbit-accuracy-experiments site; a cross-origin
// page without COEP cannot be framed by an isolated page, so the worker serves
// them under models-proxy/ (fetched with CORS, headers added), which keeps the
// framed models isolated and their threaded modules working.
const MODELS_ORIGIN = 'https://digitalarsenal.github.io/orbit-accuracy-experiments/';
let coepCredentialless = false;

function isolate(response) {
  if (response.status === 0) return response;
  const headers = new Headers(response.headers);
  headers.set('Cross-Origin-Embedder-Policy', coepCredentialless ? 'credentialless' : 'require-corp');
  if (!coepCredentialless) headers.set('Cross-Origin-Resource-Policy', 'cross-origin');
  headers.set('Cross-Origin-Opener-Policy', 'same-origin');
  return new Response(response.body, { status: response.status, statusText: response.statusText, headers });
}

if (typeof window === 'undefined') {
  self.addEventListener('install', () => self.skipWaiting());
  self.addEventListener('activate', (event) => event.waitUntil(self.clients.claim()));
  self.addEventListener('message', (event) => {
    if (!event.data) return;
    if (event.data.type === 'deregister') {
      self.registration.unregister().then(() => self.clients.matchAll()).then((clients) => clients.forEach((client) => client.navigate(client.url)));
    } else if (event.data.type === 'coepCredentialless') {
      coepCredentialless = event.data.value;
    }
  });
  self.addEventListener('fetch', (event) => {
    const request = event.request;
    if (request.cache === 'only-if-cached' && request.mode !== 'same-origin') return;
    const proxy = new URL('models-proxy/', self.registration.scope);
    if (request.url.startsWith(proxy.href)) {
      const target = MODELS_ORIGIN + request.url.slice(proxy.href.length);
      const range = request.headers.get('Range');
      event.respondWith(fetch(target, { mode: 'cors', credentials: 'omit', headers: range ? { Range: range } : {} }).then((response) => {
        const headers = new Headers(response.headers);
        headers.set('Cross-Origin-Embedder-Policy', 'require-corp');
        headers.set('Cross-Origin-Resource-Policy', 'cross-origin');
        headers.set('Cross-Origin-Opener-Policy', 'same-origin');
        return new Response(response.body, { status: response.status, statusText: response.statusText, headers });
      }));
      return;
    }
    const outgoing = coepCredentialless && request.mode === 'no-cors' ? new Request(request, { credentials: 'omit' }) : request;
    event.respondWith(fetch(outgoing).then(isolate).catch((error) => console.error(error)));
  });
} else {
  (() => {
    const options = { shouldRegister: () => true, shouldDeregister: () => false, coepCredentialless: () => !(window.chrome || window.netscape), doReload: () => window.location.reload(), quiet: false, ...window.coi };
    const sw = navigator.serviceWorker;
    if (sw && sw.controller) {
      sw.controller.postMessage({ type: 'coepCredentialless', value: options.coepCredentialless() });
      if (options.shouldDeregister()) sw.controller.postMessage({ type: 'deregister' });
    }
    if (window.crossOriginIsolated !== false || !options.shouldRegister()) return;
    if (!window.isSecureContext) return;
    if (!sw) return;
    sw.register(window.document.currentScript.src).then((registration) => {
      registration.addEventListener('updatefound', () => options.doReload());
      if (registration.active && !sw.controller) options.doReload();
    }, (error) => { if (!options.quiet) console.error('COOP/COEP Service Worker failed to register:', error); });
  })();
}

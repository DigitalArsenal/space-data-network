import { mount } from 'svelte';
import App from './App.svelte';
import { ensureCrossOriginIsolation } from './lib/cross-origin-isolation';
import './styles/app.css';

const target = document.getElementById('root');

if (!target) {
  throw new Error('SDN UI root element #root was not found');
}

// Static hosts retain the service-worker fallback. The embedded SDS $APP is
// served with COOP/COEP by the generic host adapter, so it must not request a
// second, app-local service-worker asset at runtime.
if (!import.meta.env.VITE_EMBEDDED_SDN_APP) {
  void ensureCrossOriginIsolation();
}

const app = mount(App, { target });

export default app;

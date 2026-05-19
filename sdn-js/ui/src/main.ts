import { mount } from 'svelte';
import App from './App.svelte';
import { ensureCrossOriginIsolation } from './lib/cross-origin-isolation';
import './styles/app.css';

const target = document.getElementById('root');

if (!target) {
  throw new Error('SDN UI root element #root was not found');
}

void ensureCrossOriginIsolation();

const app = mount(App, { target });

export default app;

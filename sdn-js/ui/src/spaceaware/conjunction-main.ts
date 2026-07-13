import { mount } from 'svelte';
import ConjunctionApp from './ConjunctionApp.svelte';
import './styles/spaceaware.css';

const target = document.getElementById('conj-root');

if (!target) {
  throw new Error('Conjunction UI root element #conj-root was not found');
}

const app = mount(ConjunctionApp, { target });

export default app;

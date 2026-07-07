import { mount } from 'svelte';
import SpaceAwareApp from './SpaceAwareApp.svelte';
import './styles/spaceaware.css';

const target = document.getElementById('sa-root');

if (!target) {
  throw new Error('SpaceAware UI root element #sa-root was not found');
}

const app = mount(SpaceAwareApp, { target });

export default app;

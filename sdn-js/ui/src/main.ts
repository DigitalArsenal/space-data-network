import { mount } from 'svelte';
import App from './App.svelte';
import './styles/app.css';

const target = document.getElementById('root');

if (!target) {
  throw new Error('SDN UI root element #root was not found');
}

const app = mount(App, { target });

export default app;

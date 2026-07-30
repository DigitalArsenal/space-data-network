/*
 * SDN Node Status dashboard — single-file $APP homepage.
 *
 * Data seam: the node's public read-only /ws/status feed. We start the shared
 * sdn-js status runtime (startStatusDashboard → createNodeStatusClient in
 * REMOTE mode), which decodes the size-prefixed $NST NodeStatusSet frames and
 * publishes globalThis.SDN_NODE_STATUS {subscribe,current,stop}. The Svelte
 * view reads EXCLUSIVELY from that global — it never fetches anything itself.
 *
 * Same page, any host: the ws url is derived from the page's OWN origin, so the
 * one built file works unchanged on all three SDN hosts. ?ws= overrides for dev
 * (e.g. point a locally-served page at a remote node's feed).
 */
import './fonts.css';
// The type + spacing ladder. On :root (not .root) so the wallet confirmation
// dialog, which hd-wallet-ui appends to document.body outside the app's mount,
// still resolves the tokens wallet-theme.css spends.
import './scale.css';
// The ONLY stylesheet hd-wallet-ui has (contract §11.4: the package ships
// zero css). Document-level because its confirmation dialog appends itself to
// document.body and ignores the mount.
import './wallet-theme.css';
import { mount } from 'svelte';
import App from './App.svelte';
import { startStatusDashboard } from '../../src/ui/runtime/status-dashboard';

const params = new URLSearchParams(globalThis.location?.search ?? '');
const override = (params.get('ws') ?? '').trim();
const wsUrl = override || globalThis.location?.origin || '';

// Publishes globalThis.SDN_NODE_STATUS. Remote mode → decodes $NST frames.
startStatusDashboard({ url: wsUrl });

mount(App, { target: document.getElementById('app') });

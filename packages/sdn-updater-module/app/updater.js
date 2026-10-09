// The updater page: approve waiting releases. By default the coordinator signs
// with its own node key; or an uploaded key, loaded into the updater module for
// this session only, signs. It runs in the dashboard's sandboxed module loader
// and reaches the node only through the dashboard's bridge (bridge.js).
import { createBrowserModuleHarness } from 'space-data-module-sdk/host/browser-module';
import { callHost, request } from './bridge.js';

const ROUTE = '/api/v1/admin/updates/distributions';
const IDLE_MS = 15 * 60 * 1000;
const encoder = new TextEncoder(), decoder = new TextDecoder();
const $ = id => document.getElementById(id);
let module, uploaded = null, idle, roots = [];
const approved = new Set(); // update ids approved on this page; their signals follow

function status(id, text, error = false) { $(id).textContent = text; $(id).classList.toggle('error', error); }

async function invoke(methodId, body) {
  const payload = encoder.encode(JSON.stringify(body));
  const response = await module.invoke({ methodId, inputs: [{ portId: 'request', typeRef: { wireFormat: 'aligned-binary', requiredAlignment: 1, byteLength: payload.length }, payload }] });
  const out = response.outputs?.find(f => f.portId === 'result')?.payload;
  const text = out ? decoder.decode(out) : '';
  const parsed = text.startsWith('{"error":') ? JSON.parse(text) : null;
  if (response.statusCode !== 0 || parsed?.error) throw new Error(parsed?.error || response.errorMessage || `${methodId} failed`);
  return text;
}

async function closeKey(reason) {
  clearTimeout(idle);
  if (module && uploaded) await invoke('closeKey', {}).catch(() => {});
  uploaded = null;
  $('key-form').hidden = false; $('key-open').hidden = true; $('node-key').hidden = false;
  if (reason) status('key-status', reason);
}

function touch() { clearTimeout(idle); if (uploaded) idle = setTimeout(() => closeKey('The uploaded key was wiped after 15 minutes without use.'), IDLE_MS); }

$('key-file').addEventListener('change', async () => {
  const input = $('key-file'), file = input.files?.[0];
  input.value = '';
  if (!file) return;
  const body = { key_file: await file.text() };
  try {
    uploaded = JSON.parse(await invoke('openKey', body));
    body.key_file = '';
    $('key-id').textContent = uploaded.key_id; $('key-public').textContent = uploaded.public_key;
    $('key-form').hidden = true; $('key-open').hidden = false; $('node-key').hidden = true;
    status('key-status', roots.includes(uploaded.key_id) ? 'Approvals are now signed with the uploaded key.' :
      'This key is not one of the fleet’s update roots: nodes will refuse what it signs until a release signed with a trusted key adds it.', !roots.includes(uploaded.key_id));
    touch();
  } catch (error) { uploaded = null; status('key-status', error.message, true); }
});
$('close').addEventListener('click', () => closeKey('The uploaded key was wiped. Approvals are signed with the node key.'));
window.addEventListener('pagehide', () => { if (uploaded) invoke('closeKey', {}).catch(() => {}); });

async function readJSON(response) { return JSON.parse(decoder.decode(new Uint8Array(await response.arrayBuffer()))); }

async function approve(release, kind) {
  touch();
  let path = `${ROUTE}/${release.id}/approve`, body = encoder.encode(JSON.stringify({ document: kind }));
  if (uploaded) {
    path = `${ROUTE}/${release.id}/${kind}`;
    body = encoder.encode(await invoke('signRelease', { kind, document: kind === 'manifest' ? release.manifest : release.signal }));
  }
  const response = await request(path, { method: 'POST', body });
  const reply = await readJSON(response);
  if (!response.ok) throw new Error(reply?.error?.message || `The node refused the ${kind}.`);
  if (kind === 'manifest') approved.add(release.update_id);
  return reply;
}

let lastList = [];
function escape(text) { return String(text ?? '').replace(/[&<>"']/g, c => `&#${c.charCodeAt(0)};`); }
function render(list) {
  lastList = list;
  $('releases').innerHTML = list.map(r =>
    `<tr><td><div>${escape(r.version)}</div><div class="muted mono">${escape(r.update_id)}</div></td><td>${escape(r.target)}</td>` +
    `<td>${r.recipients || 'all'}</td><td>${escape(r.state.replaceAll('-', ' '))}</td>` +
    `<td>${r.state === 'awaiting-manifest-signature' ? `<button class="primary" data-id="${escape(r.id)}">Approve</button>` : ''}</td></tr>`).join('');
  status('list-status', list.length ? '' : 'No release is waiting for approval.');
}

$('releases').addEventListener('click', async event => {
  const release = lastList.find(r => r.id === event.target?.dataset?.id);
  if (!release) return;
  event.target.disabled = true;
  try { await approve(release, 'manifest'); status('key-status', `Release ${release.version} approved. Its signal is approved when the files are served.`); }
  catch (error) { status('key-status', error.message, true); }
  await refresh();
});

async function refresh() {
  try {
    const response = await request(ROUTE);
    if (!response.ok) throw new Error(response.status === 404 ? 'This node does not coordinate releases.' : 'The node did not list its releases.');
    const listed = await readJSON(response);
    roots = listed.update_roots || [];
    $('node-key-id').textContent = listed.node_key_id || '';
    // A release approved here is signalled with the same key; then an uploaded key is wiped.
    for (const release of listed.distributions) {
      if (release.state === 'awaiting-signal-signature' && approved.has(release.update_id)) {
        await approve(release, 'signal');
        approved.delete(release.update_id);
        status('key-status', `Release ${release.version} signalled to the fleet.`);
        if (uploaded && approved.size === 0) await closeKey(`Release ${release.version} signalled to the fleet. The uploaded key was wiped.`);
      }
    }
    render(listed.distributions);
  } catch (error) { status('list-status', error.message, true); }
}

try {
  const artifact = await callHost('module');
  module = await createBrowserModuleHarness({ wasmSource: artifact.bytes, manifest: __PLUGIN_MANIFEST__, surface: 'direct' });
  await refresh();
  setInterval(refresh, 4000);
} catch (error) { status('list-status', error.message, true); }

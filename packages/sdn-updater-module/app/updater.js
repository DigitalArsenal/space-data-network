// The updater page: enter the distribution key, sign waiting releases, wipe the key.
// It runs in the dashboard's sandboxed module loader and reaches the node only
// through the dashboard's bridge (bridge.js).
import { createBrowserModuleHarness } from 'space-data-module-sdk/host/browser-module';
import { callHost, request } from './bridge.js';

const ROUTE = '/api/v1/admin/updates/distributions';
const IDLE_MS = 15 * 60 * 1000;
const encoder = new TextEncoder(), decoder = new TextDecoder();
const $ = id => document.getElementById(id);
let module, key = null, idle, polling;
const approved = new Set(); // update ids whose manifest this key signed

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
  if (module && key) await invoke('closeKey', {}).catch(() => {});
  key = null; approved.clear();
  $('key-form').hidden = false; $('key-open').hidden = true;
  if (reason) status('key-status', reason);
  render(lastList);
}

function touch() { clearTimeout(idle); idle = setTimeout(() => closeKey('The key was closed after 15 minutes without use.'), IDLE_MS); }

$('key-form').addEventListener('submit', async event => {
  event.preventDefault();
  const phrase = $('phrase'), passphrase = $('passphrase');
  const body = { phrase: phrase.value, passphrase: passphrase.value };
  phrase.value = ''; passphrase.value = '';
  status('key-status', 'Deriving the key');
  try {
    key = JSON.parse(await invoke('openKey', body));
    body.phrase = body.passphrase = '';
    $('key-id').textContent = key.key_id; $('key-path').textContent = key.path; $('key-public').textContent = key.public_key;
    $('key-form').hidden = true; $('key-open').hidden = false;
    status('key-status', 'Key entered. Sign a waiting release below.');
    touch(); render(lastList);
  } catch (error) { key = null; status('key-status', error.message, true); }
});
$('close').addEventListener('click', () => closeKey('Key closed.'));
window.addEventListener('pagehide', () => { if (key) invoke('closeKey', {}).catch(() => {}); });

async function readJSON(response) { return JSON.parse(decoder.decode(new Uint8Array(await response.arrayBuffer()))); }

async function sign(release, kind) {
  touch();
  const unsigned = kind === 'manifest' ? release.manifest : release.signal;
  const signed = await invoke('signRelease', { kind, document: unsigned });
  const response = await request(`${ROUTE}/${release.id}/${kind}`, { method: 'POST', body: encoder.encode(signed) });
  const reply = await readJSON(response);
  if (!response.ok) throw new Error(reply?.error?.message || `The node refused the ${kind} signature.`);
  if (kind === 'manifest') approved.add(release.update_id);
  return reply;
}

let lastList = [];
function render(list) {
  lastList = list;
  const rows = list.map(r => {
    const action = r.state === 'awaiting-manifest-signature' ? 'Sign release' : '';
    return `<tr><td><div>${escape(r.version)}</div><div class="muted mono">${escape(r.update_id)}</div></td><td>${escape(r.target)}</td>` +
      `<td>${r.recipients || 'all'}</td><td>${escape(r.state.replaceAll('-', ' '))}</td>` +
      `<td>${action ? `<button data-id="${escape(r.id)}" ${key ? '' : 'disabled'}>${action}</button>` : ''}</td></tr>`;
  });
  $('releases').innerHTML = rows.join('');
  status('list-status', list.length ? '' : 'No release is waiting for the key.');
}
function escape(text) { return String(text ?? '').replace(/[&<>"']/g, c => `&#${c.charCodeAt(0)};`); }

$('releases').addEventListener('click', async event => {
  const id = event.target?.dataset?.id;
  const release = lastList.find(r => r.id === id);
  if (!release || !key) return;
  event.target.disabled = true;
  try { await sign(release, 'manifest'); status('key-status', `Release ${release.version} signed. Its signal is signed when the files are served.`); }
  catch (error) { status('key-status', error.message, true); }
  await refresh();
});

async function refresh() {
  try {
    const response = await request(ROUTE);
    if (!response.ok) throw new Error(response.status === 404 ? 'This node does not coordinate releases.' : 'The node did not list its releases.');
    const { distributions } = await readJSON(response);
    // A release this key approved is signalled with the same key, then the key closes.
    for (const release of distributions) {
      if (key && release.state === 'awaiting-signal-signature' && approved.has(release.update_id)) {
        await sign(release, 'signal');
        approved.delete(release.update_id);
        status('key-status', `Release ${release.version} signalled to the fleet.`);
        if (approved.size === 0) await closeKey(`Release ${release.version} signalled to the fleet. The key was closed.`);
      }
    }
    render(distributions);
  } catch (error) { status('list-status', error.message, true); }
}

try {
  const artifact = await callHost('module');
  module = await createBrowserModuleHarness({ wasmSource: artifact.bytes, manifest: __PLUGIN_MANIFEST__, surface: 'direct' });
  await refresh();
  polling = setInterval(refresh, 4000);
} catch (error) { status('list-status', error.message, true); }

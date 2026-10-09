// The dashboard owns the node connection. The sandboxed page gets one
// MessagePort, never admin credentials (dashboard data/module-app.js).
let port;
let sequence = 0;
const pending = new Map();
const ready = new Promise(resolve => {
  window.addEventListener('message', function connect(event) {
    if (event.source !== window.parent || event.data?.type !== 'sdn-app-connect' || event.ports.length !== 1 || port) return;
    port = event.ports[0];
    port.onmessage = ({ data }) => {
      const call = pending.get(data?.id);
      if (!call) return;
      pending.delete(data.id);
      clearTimeout(call.timer);
      data.error ? call.reject(new Error(data.error)) : call.resolve(data.value);
    };
    window.removeEventListener('message', connect);
    resolve();
  });
  window.parent.postMessage({ type: 'sdn-app-ready' }, '*');
});

export async function callHost(method, params = {}) {
  await ready;
  const id = ++sequence;
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => { pending.delete(id); port.postMessage({ id, method: 'cancel' }); reject(new Error('The node took too long to respond.')); }, 30000);
    pending.set(id, { resolve, reject, timer });
    port.postMessage({ id, method, params });
  });
}

export async function request(path, options = {}) {
  const result = await callHost('request', { path, method: options.method || 'GET', body: options.body });
  return { ok: result.status >= 200 && result.status < 300, status: result.status,
    arrayBuffer: async () => result.body.buffer.slice(result.body.byteOffset, result.body.byteOffset + result.body.byteLength) };
}

import type { AdminSnapshot } from '../../../src/ui/runtime/admin-state';
import { escapeHtml } from '../dom/escape';
import type { DirectoryUserLike } from '../state/types';

export function renderDirectorySummary(snapshot: AdminSnapshot): string {
  return `
    <div class="sdn-stack">
      <strong>${escapeHtml(snapshot.nodeContext.displayName)}</strong>
      <span>Mode: ${escapeHtml(snapshot.mode)}</span>
      <span>Role: ${escapeHtml(snapshot.permissions.role)}</span>
      <span>Transport: ${escapeHtml(snapshot.nodeContext.transport)}</span>
      <span>Peer ID: ${escapeHtml(snapshot.nodeContext.peerId ?? '<unknown>')}</span>
      <span>Server: ${escapeHtml(snapshot.serverTarget?.baseUrl ?? '<browser-local>')}</span>
    </div>
  `;
}

export function renderDirectoryUsers(summary: string, users: DirectoryUserLike[]): string {
  return `
    ${summary}
    <div class="sdn-stack">
      <strong>Server users (${users.length})</strong>
      ${users.map((user) => `
        <div class="sdn-sighting">
          <strong>${escapeHtml(user.name ?? user.xpub)}</strong>
          <span>${escapeHtml(user.trust_level ?? 'unknown')} · ${escapeHtml(user.source ?? 'server')}</span>
          <span>${escapeHtml(user.xpub)}</span>
        </div>
      `).join('')}
    </div>
  `;
}

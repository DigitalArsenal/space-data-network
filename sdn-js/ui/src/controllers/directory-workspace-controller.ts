import { query } from '../dom/query';
import { escapeHtml, formatError } from '../dom/escape';
import type { AppState } from '../state/app-state';
import type { DirectoryUserLike } from '../state/types';
import { renderDirectorySummary, renderDirectoryUsers } from '../views/directory-view';

interface DirectoryWorkspaceControllerOptions {
  root: HTMLElement;
  state: AppState;
}

export function createDirectoryWorkspaceController(
  options: DirectoryWorkspaceControllerOptions,
) {
  const { root, state } = options;

  async function refreshDirectoryPanel(): Promise<void> {
    const panel = query<HTMLElement>(root, '#sdn-directory-panel');
    const admin = state.admin;
    if (!panel || !admin) {
      return;
    }

    const snapshot = admin.snapshot();
    const summary = renderDirectorySummary(snapshot);

    if (snapshot.mode === 'local') {
      panel.innerHTML = `
        ${summary}
        <div class="sdn-empty">
          Local mode uses the browser-owned Helia backend. Switch to Server to inspect the live node directory and user roster.
        </div>
      `;
      return;
    }

    if (!snapshot.permissions.authenticated || !snapshot.serverTarget?.baseUrl) {
      panel.innerHTML = `
        ${summary}
        <div class="sdn-empty">
          Sign in on the selected server to inspect node membership and permissions.
        </div>
      `;
      return;
    }

    if (snapshot.permissions.role !== 'admin') {
      panel.innerHTML = `
        ${summary}
        <div class="sdn-empty">
          Connected as ${escapeHtml(snapshot.permissions.role)}. Admin-only user management data is hidden for this session.
        </div>
      `;
      return;
    }

    panel.innerHTML = `
      ${summary}
      <div class="sdn-empty">Loading live server users…</div>
    `;

    try {
      const response = await fetch(`${snapshot.serverTarget.baseUrl}/api/auth/users`, {
        credentials: 'include',
      });
      if (!response.ok) {
        throw new Error(`user query failed (${response.status})`);
      }
      const users = await response.json() as DirectoryUserLike[];
      panel.innerHTML = renderDirectoryUsers(summary, users);
    } catch (error) {
      panel.innerHTML = `
        ${summary}
        <div class="sdn-empty">${escapeHtml(formatError(error))}</div>
      `;
    }
  }

  return {
    refreshDirectoryPanel,
  };
}

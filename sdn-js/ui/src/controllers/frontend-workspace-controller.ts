import {
  createFrontendWorkspace,
  createServerFrontendTransport,
  type FrontendUploadFile,
  type FrontendWorkspace,
  type FrontendWorkspaceSnapshot,
} from '../../../src/ui/runtime/frontend-workspace';
import {
  createBrowserEditorController,
  type FrontendEditorController,
} from '../frontend-editor';
import { query } from '../dom/query';
import { escapeHtml } from '../dom/escape';
import type { AppState } from '../state/app-state';
import {
  frontendSelectedDirectory,
  renderFrontendStatusText,
  renderFrontendTree,
} from '../views/frontend-view';

interface FrontendWorkspaceControllerOptions {
  root: HTMLElement;
  state: AppState;
}

export function createFrontendWorkspaceController(
  options: FrontendWorkspaceControllerOptions,
) {
  const { root, state } = options;

  async function refreshFrontendWorkspace(): Promise<void> {
    const admin = state.admin;
    const statusNode = query<HTMLElement>(root, '#sdn-frontend-status');
    if (!admin || !statusNode) {
      return;
    }

    const adminSnapshot = admin.snapshot();
    if (
      adminSnapshot.mode === 'server'
      && (
        !adminSnapshot.serverTarget?.baseUrl
        || !adminSnapshot.permissions.authenticated
        || adminSnapshot.permissions.role !== 'admin'
      )
    ) {
      state.frontendWorkspace = null;
      state.frontendWorkspaceKey = null;
      renderFrontendPlaceholder(
        'Connect as an admin on the selected server to manage the public frontend.',
      );
      return;
    }

    const workspaceKey = adminSnapshot.mode === 'server'
      ? `server:${adminSnapshot.serverTarget?.baseUrl ?? ''}`
      : 'local';

    if (!state.frontendWorkspace || state.frontendWorkspaceKey !== workspaceKey) {
      state.frontendWorkspace = createFrontendWorkspace({
        mode: adminSnapshot.mode,
        transport: adminSnapshot.mode === 'server' && adminSnapshot.serverTarget?.baseUrl
          ? createServerFrontendTransport({ baseUrl: adminSnapshot.serverTarget.baseUrl })
          : state.localFrontendTransport,
      });
      state.frontendWorkspaceKey = workspaceKey;
      await state.frontendWorkspace.connect();
    }

    await ensureFrontendEditor();
    renderFrontendWorkspace(state.frontendWorkspace.snapshot());
  }

  async function ensureFrontendEditor(): Promise<void> {
    if (state.frontendEditor) {
      return;
    }
    const host = query<HTMLElement>(root, '#sdn-frontend-editor');
    if (!host) {
      return;
    }
    state.frontendEditor = await createBrowserEditorController(host, (value) => {
      if (!state.frontendWorkspace) {
        return;
      }
      const snapshot = state.frontendWorkspace.editContent(value);
      renderFrontendStatus(snapshot);
    });
  }

  function renderFrontendWorkspace(snapshot: FrontendWorkspaceSnapshot): void {
    const pathInput = query<HTMLInputElement>(root, '#sdn-frontend-path');
    const tree = query<HTMLElement>(root, '#sdn-frontend-tree');
    const editor = state.frontendEditor;
    if (pathInput) {
      pathInput.value = snapshot.selectedPath ?? '';
    }
    renderFrontendStatus(snapshot);
    if (tree) {
      tree.innerHTML = renderFrontendTree(snapshot);
    }
    editor?.setDocument(snapshot.editor.value, snapshot.editor.language);
  }

  function renderFrontendStatus(snapshot: FrontendWorkspaceSnapshot): void {
    const statusNode = query<HTMLElement>(root, '#sdn-frontend-status');
    const saveButton = query<HTMLButtonElement>(root, '#sdn-frontend-save');
    const deleteButton = query<HTMLButtonElement>(root, '#sdn-frontend-delete');
    const moveButton = query<HTMLButtonElement>(root, '#sdn-frontend-move');
    if (statusNode) {
      statusNode.textContent = renderFrontendStatusText(snapshot);
    }
    if (saveButton) {
      saveButton.disabled = !snapshot.selectedPath || !snapshot.editor.dirty;
    }
    if (deleteButton) {
      deleteButton.disabled = !snapshot.selectedPath;
    }
    if (moveButton) {
      moveButton.disabled = !snapshot.selectedPath;
    }
  }

  function renderFrontendPlaceholder(message: string): void {
    const tree = query<HTMLElement>(root, '#sdn-frontend-tree');
    const status = query<HTMLElement>(root, '#sdn-frontend-status');
    const pathInput = query<HTMLInputElement>(root, '#sdn-frontend-path');
    if (tree) {
      tree.innerHTML = `<div class="sdn-empty">${escapeHtml(message)}</div>`;
    }
    if (status) {
      status.textContent = message;
    }
    if (pathInput) {
      pathInput.value = '';
    }
    state.frontendEditor?.setDocument('', 'plaintext');
  }

  async function selectFrontendFile(path: string): Promise<void> {
    if (!state.frontendWorkspace) {
      return;
    }
    renderFrontendWorkspace(await state.frontendWorkspace.selectPath(path));
    state.frontendEditor?.focus();
  }

  async function saveFrontendFile(): Promise<void> {
    if (!state.frontendWorkspace) {
      return;
    }
    renderFrontendWorkspace(await state.frontendWorkspace.save());
  }

  async function moveFrontendFile(): Promise<void> {
    if (!state.frontendWorkspace) {
      return;
    }
    const targetPath = query<HTMLInputElement>(root, '#sdn-frontend-path')?.value.trim() ?? '';
    if (!targetPath) {
      return;
    }
    renderFrontendWorkspace(await state.frontendWorkspace.moveSelection(targetPath));
  }

  async function deleteFrontendFile(): Promise<void> {
    if (!state.frontendWorkspace) {
      return;
    }
    renderFrontendWorkspace(await state.frontendWorkspace.deleteSelection());
  }

  async function uploadFrontendFiles(input: HTMLInputElement | null): Promise<void> {
    const files = [...(input?.files ?? [])];
    if (files.length === 0) {
      return;
    }
    await uploadFrontendFileList(files);
    if (input) {
      input.value = '';
    }
  }

  async function uploadFrontendFileList(files: File[]): Promise<void> {
    if (!state.frontendWorkspace) {
      return;
    }
    const uploads = await Promise.all(files.map(async (file) => ({
      name: file.name,
      text: await file.text(),
    } satisfies FrontendUploadFile)));
    const directory = frontendSelectedDirectory(state.frontendWorkspace.snapshot().selectedPath);
    renderFrontendWorkspace(await state.frontendWorkspace.upload(uploads, directory));
  }

  return {
    deleteFrontendFile,
    refreshFrontendWorkspace,
    saveFrontendFile,
    selectFrontendFile,
    uploadFrontendFileList,
    uploadFrontendFiles,
    moveFrontendFile,
  };
}

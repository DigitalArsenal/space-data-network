import type {
  FrontendTreeNode,
  FrontendWorkspaceSnapshot,
} from '../../../src/ui/runtime/frontend-workspace';
import { escapeHtml } from '../dom/escape';

export function renderFrontendTree(snapshot: FrontendWorkspaceSnapshot): string {
  return snapshot.tree.length === 0
    ? '<div class="sdn-empty">No frontend files available.</div>'
    : `<ul class="sdn-frontend-tree__list">${renderFrontendTreeNodes(snapshot.tree, snapshot.selectedPath)}</ul>`;
}

export function renderFrontendStatusText(snapshot: FrontendWorkspaceSnapshot): string {
  return snapshot.editor.dirty
    ? `${snapshot.status} · unsaved`
    : snapshot.status;
}

export function renderFrontendTreeNodes(
  nodes: FrontendTreeNode[],
  selectedPath: string | null,
): string {
  return nodes.map((node) => `
    <li class="sdn-frontend-tree__node">
      <button
        type="button"
        class="sdn-frontend-tree__item${node.path === selectedPath ? ' sdn-frontend-tree__item--active' : ''}"
        data-frontend-path="${escapeHtml(node.path)}"
      >
        <span class="sdn-frontend-tree__badge">${node.isDir ? 'DIR' : 'FILE'}</span>
        <span>${escapeHtml(node.name)}</span>
      </button>
      ${node.children?.length
        ? `<ul class="sdn-frontend-tree__list">${renderFrontendTreeNodes(node.children, selectedPath)}</ul>`
        : ''}
    </li>
  `).join('');
}

export function frontendSelectedDirectory(selectedPath: string | null): string {
  if (!selectedPath || !selectedPath.includes('/')) {
    return '';
  }
  return selectedPath.split('/').slice(0, -1).join('/');
}

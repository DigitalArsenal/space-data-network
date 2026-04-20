import { queryAll } from './query';

export function setActiveWorkspace(root: HTMLElement, workspaceId: string): void {
  queryAll(root, '[data-nav]').forEach((item) => {
    item.classList.toggle(
      'sdn-admin-nav__item--active',
      item.getAttribute('data-nav') === workspaceId,
    );
  });
  queryAll(root, '[data-workspace]').forEach((panel) => {
    panel.classList.toggle(
      'sdn-admin-workspace--active',
      panel.getAttribute('data-workspace') === workspaceId,
    );
  });
}

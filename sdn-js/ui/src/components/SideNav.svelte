<script lang="ts">
  import { onMount } from 'svelte';

  export let activeRoute = '/node';

  const navItems = [
    { href: '#/node', route: '/node', label: 'Node' },
    { href: '#/peers', route: '/peers', label: 'Peers' },
    { href: '#/data', route: '/data', label: 'Data' },
    { href: '#/channels', route: '/channels', label: 'Channels' },
    { href: '#/conjunction', route: '/conjunction', label: 'Conjunction' },
  ] as const;

  type RegisteredApp = {
    slug: string;
    name: string;
    description?: string;
    path: string;
    root?: boolean;
  };

  // Apps registered with the node (GET /apps/index.json). The console never
  // hardcodes sibling apps — the node's serving registry is the source of
  // truth. The root app (this console) is excluded from its own submenu.
  let registeredApps: RegisteredApp[] = [];

  onMount(async () => {
    try {
      const response = await fetch('/apps/index.json', { cache: 'no-store' });
      if (!response.ok) return;
      const payload = (await response.json()) as { apps?: RegisteredApp[] };
      registeredApps = (payload.apps ?? []).filter((app) => !app.root);
    } catch {
      // Registry unavailable (e.g. dev server without the node) — no submenu.
    }
  });
</script>

<aside class="sdn-side-nav" aria-label="Primary">
  <div class="sdn-brand">
    <svg class="sdn-brand-mark" viewBox="0 0 128 128" role="img" aria-label="Space Data Network">
      <circle cx="64" cy="64" r="52" fill="none" stroke="currentColor" stroke-width="9" />
      <path d="M64 116 L19 38 L109 38 Z" fill="none" stroke="currentColor" stroke-width="10" stroke-linejoin="round" />
      <circle cx="64" cy="64" r="7" fill="currentColor" />
    </svg>
    <div class="sdn-brand-text">
      <strong>SDN</strong>
      <span>Space Data Network</span>
    </div>
  </div>
  <nav class="sdn-nav-list" aria-label="Primary">
    {#each navItems as item}
      <a
        class="sdn-nav-link"
        href={item.href}
        aria-current={activeRoute === item.route ? 'page' : undefined}
      >
        {item.label}
      </a>
    {/each}
  </nav>
  {#if registeredApps.length > 0}
    <nav class="sdn-nav-list sdn-nav-apps" aria-label="Apps">
      <span class="sdn-nav-section-label">Apps</span>
      {#each registeredApps as app (app.slug)}
        <a class="sdn-nav-link" href={app.path} title={app.description ?? app.name}>
          {app.name}
        </a>
      {/each}
    </nav>
  {/if}
</aside>

<style>
  .sdn-nav-apps {
    margin-top: 14px;
    padding-top: 12px;
    border-top: 1px solid rgba(140, 170, 210, 0.22);
  }

  .sdn-nav-section-label {
    display: block;
    padding: 0 12px 6px;
    font-size: 10px;
    font-weight: 600;
    letter-spacing: 0.16em;
    text-transform: uppercase;
    opacity: 0.65;
  }
</style>

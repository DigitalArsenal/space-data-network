<script lang="ts">
  export let activeRoute = '/node';

  const navItems = [
    { href: '#/node', route: '/node', label: 'Node' },
    { href: '#/peers', route: '/peers', label: 'Peers' },
    { href: '#/data', route: '/data', label: 'Data' },
    { href: '#/channels', route: '/channels', label: 'Channels' },
    { href: '#/conjunction', route: '/conjunction', label: 'Conjunction' },
  ] as const;
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
</aside>

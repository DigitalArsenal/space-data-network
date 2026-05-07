<script lang="ts">
  export let activeRoute = '/node';

  const navItems = [
    { href: '#/node', route: '/node', label: 'Node' },
    { href: '#/peers', route: '/peers', label: 'Peers' },
    { href: '#/local-data', route: '/local-data', label: 'Local Data' },
  ] as const;
</script>

<aside class="sdn-side-nav" aria-label="Primary">
  <div class="sdn-brand">
    <strong>SDN</strong>
    <span>Space Data Network</span>
  </div>
  <nav class="sdn-nav-list">
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

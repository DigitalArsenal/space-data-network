<script lang="ts">
  import Panel from '../primitives/Panel.svelte';
  import SaButton from '../primitives/SaButton.svelte';
  import SaTabs from '../primitives/SaTabs.svelte';
  import { BMC2_MODES, type SpaceAwareRoute } from '../router';
  import type { AuthSessionState } from '../../lib/auth/auth-store';

  let {
    route,
    navigate,
  }: {
    route: SpaceAwareRoute;
    navigate: (path: string) => void;
    authState?: AuthSessionState;
  } = $props();

  // U3.1 note: `route.screen === 'console'` no longer reaches this
  // component — `SpaceAwareApp.svelte` routes it to `ConsoleShell.svelte`
  // instead, which has its own real header chips (health/session). The
  // `screenMeta.console` entry below only survives as the generic
  // fallback value for an unrecognized `route.screen`.
  const screenMeta: Record<string, { title: string; portedIn: string; reference: string }> = {
    console: {
      title: 'SDN CONSOLE',
      portedIn: 'U3.1–U3.9',
      reference: 'sdn_console/SDN Console.dc.html',
    },
    orbital: {
      title: 'ORBITAL CONSOLE',
      portedIn: 'U5.1–U5.4',
      reference: 'orbital_console/Orbital Console.dc.html',
    },
    gantt: {
      title: 'GANTT VIEW',
      portedIn: 'U5.5',
      reference: 'gantt_view/Gantt View.dc.html',
    },
    bmc2: {
      title: 'BMC2 MODE BOARDS',
      portedIn: 'U2.1–U2.2',
      reference: 'bmc2/BMC2_Modes_Index.dc.html + F1–F6',
    },
  };

  const meta = $derived(screenMeta[route.screen] ?? screenMeta.console);

  const bmc2Tabs = [
    { id: 'index', label: 'INDEX', title: 'BMC2 modes index scaffold' },
    ...BMC2_MODES.map((mode) => ({
      id: mode,
      label: mode.toUpperCase(),
      title: `BMC2 ${mode.toUpperCase()} board scaffold`,
    })),
  ];
</script>

<main>
  <header>
    <span class="sa-kicker">SpaceAware · SDN · Scaffold</span>
    <h1>{meta.title}</h1>
    <p class="scaffold-note">
      Route skeleton placeholder ({route.path}). The pixel port of
      {meta.reference} lands in loop {meta.portedIn}.
    </p>
  </header>

  {#if route.screen === 'bmc2'}
    <SaTabs
      tabs={bmc2Tabs}
      selected={route.sub ?? 'index'}
      onselect={(mode) => navigate(mode === 'index' ? '/bmc2' : `/bmc2/${mode}`)}
    />
  {/if}

  <Panel title={`${meta.title} · not yet ported`} variant="well">
    <p class="empty-copy">
      This surface is intentionally empty: no placeholder data is rendered
      until the screen is wired to real surfaces per the loop plan.
    </p>
  </Panel>

  <nav class="route-nav">
    <span class="sa-kicker">Navigate</span>
    <div class="btn-row">
      <SaButton variant="neutral" title="Login gate" onclick={() => navigate('/login')}>Login</SaButton>
      <SaButton variant="neutral" title="SDN console scaffold" onclick={() => navigate('/console/node')}>
        Console
      </SaButton>
      <SaButton variant="neutral" title="Orbital console scaffold" onclick={() => navigate('/orbital')}>
        Orbital
      </SaButton>
      <SaButton variant="neutral" title="Gantt view scaffold" onclick={() => navigate('/gantt')}>
        Gantt
      </SaButton>
      <SaButton variant="neutral" title="BMC2 mode boards scaffold" onclick={() => navigate('/bmc2')}>
        BMC2
      </SaButton>
    </div>
  </nav>
</main>

<style>
  main {
    max-width: 1080px;
    margin: 0 auto;
    padding: 40px 24px 64px 24px;
    display: flex;
    flex-direction: column;
    gap: 20px;
  }
  h1 {
    margin: 6px 0 0 0;
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 700;
    font-size: 26px;
    letter-spacing: 0.16em;
    color: #eaf6f8;
  }
  .scaffold-note {
    margin: 8px 0 0 0;
    max-width: 640px;
    font-size: 11px;
    color: #7d929b;
  }
  .empty-copy {
    margin: 0;
    font-size: 11px;
    color: #5d7681;
  }
  .route-nav {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .btn-row {
    display: flex;
    gap: 12px;
    flex-wrap: wrap;
  }
</style>

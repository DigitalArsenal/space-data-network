<script lang="ts">
  /**
   * SdnGlobe demo panel (loop U0.2) — hosted on the /console/peers scaffold
   * until the real PEER MAP wiring lands in U3.4.
   *
   * The HOME/CONNECTIONS fixtures below are the design handoff's mock data
   * (sdn_console/SDN Console.dc.html) and are clearly demo-labeled; nothing
   * here pretends to be live peer telemetry.
   */
  import {
    sdnGlobe,
    type SdnGlobeHome,
    type SdnGlobeMode,
    type SdnGlobePoint,
  } from '../../lib/globe/SdnGlobe';

  // Demo fixtures — verbatim from the design handoff (.dc.html ground truth).
  const HOME: SdnGlobeHome = {
    lat: 38.83,
    lon: -104.82,
    label: 'THIS NODE',
    city: 'Colorado Springs, US',
  };
  const CONNECTIONS: SdnGlobePoint[] = [
    { lat: 37.77, lon: -122.42, kind: 'provider', label: 'SpaceAware.io', city: 'San Francisco, US', ip: '159.203.150.8' },
    { lat: 51.51, lon: -0.13, kind: 'provider', label: 'CelesTrak', city: 'London, GB', ip: '167.172.219.213' },
    { lat: 50.11, lon: 8.68, kind: 'peer', label: 'OrbitalEdge', city: 'Frankfurt, DE', ip: '138.68.31.4' },
    { lat: 40.71, lon: -74.01, kind: 'peer', label: 'LaGrange Relay', city: 'New York, US', ip: '45.55.124.9' },
    { lat: 35.68, lon: 139.69, kind: 'client', city: 'Tokyo, JP', ip: '133.242.14.2' },
    { lat: 1.35, lon: 103.82, kind: 'client', city: 'Singapore, SG', ip: '128.199.84.7' },
    { lat: -33.87, lon: 151.21, kind: 'client', city: 'Sydney, AU', ip: '170.64.130.9' },
    { lat: -23.55, lon: -46.63, kind: 'client', city: 'Sao Paulo, BR', ip: '168.181.49.3' },
    { lat: 43.65, lon: -79.38, kind: 'client', city: 'Toronto, CA', ip: '159.89.112.6' },
    { lat: 52.37, lon: 4.9, kind: 'client', city: 'Amsterdam, NL', ip: '188.166.20.4' },
    { lat: 12.97, lon: 77.59, kind: 'client', city: 'Bengaluru, IN', ip: '139.59.24.8' },
    { lat: -33.92, lon: 18.42, kind: 'client', city: 'Cape Town, ZA', ip: '154.0.13.7' },
    { lat: 37.57, lon: 126.98, kind: 'client', city: 'Seoul, KR', ip: '158.247.203.1' },
    { lat: 25.2, lon: 55.27, kind: 'client', city: 'Dubai, AE', ip: '185.93.2.9' },
    { lat: 59.33, lon: 18.06, kind: 'client', city: 'Stockholm, SE', ip: '46.246.30.5' },
    { lat: 19.43, lon: -99.13, kind: 'client', city: 'Mexico City, MX', ip: '187.141.8.2' },
  ];

  // Design's connColor: provider cyan, peer light blue, client green.
  function connColor(kind: string | undefined): string {
    return kind === 'provider' ? '#35c9d8' : kind === 'peer' ? '#9fd4f5' : '#5ad6a0';
  }

  let mapMode = $state<SdnGlobeMode>('3d');

  function countryCount(): number {
    const s: Record<string, 1> = {};
    const add = (c: { city?: string }) => {
      const cc = ((c && c.city) || '').split(', ')[1] || '';
      if (cc) s[cc] = 1;
    };
    add(HOME);
    CONNECTIONS.forEach(add);
    return Object.keys(s).length;
  }

  const globeOptions = $derived({
    home: HOME,
    points: CONNECTIONS,
    colorFor: connColor,
    // Reading mapMode HERE (not just inside a closure) makes the derived
    // object invalidate on toggle, so the action's update applies the mode.
    mode: mapMode,
  });
</script>

<section class="netmap" aria-label="Peer map demo">
  <header class="netmap-head">
    <span class="netmap-title" title="Canvas peer map (globe.js port) — demo fixtures until U3.4">
      PEER MAP · GEOIP <span class="demo-tag" title="Design-handoff mock connections; real peer wiring lands in loop U3.4">DEMO FIXTURES</span>
    </span>
    <div class="mode-tabs" role="tablist" aria-label="Map projection">
      <button
        type="button"
        role="tab"
        aria-selected={mapMode === '3d'}
        class:active={mapMode === '3d'}
        title="3D globe projection"
        onclick={() => (mapMode = '3d')}
      >3D</button>
      <button
        type="button"
        role="tab"
        aria-selected={mapMode === '2d'}
        class:active={mapMode === '2d'}
        title="2D equirectangular map"
        onclick={() => (mapMode = '2d')}
      >2D</button>
    </div>
  </header>

  <div class="netmap-body">
    <canvas use:sdnGlobe={globeOptions} data-testid="sdn-globe-canvas"></canvas>
    <div class="netmap-counts" aria-hidden="true">
      <div>{CONNECTIONS.length} CONNECTIONS</div>
      <div>{countryCount()} COUNTRIES</div>
    </div>
    <div class="netmap-legend" aria-hidden="true">
      <span><i style="background:#35c9d8"></i>PROVIDER</span>
      <span><i style="background:#9fd4f5"></i>PEER</span>
      <span><i style="background:#5ad6a0"></i>CLIENT</span>
      <span><i style="background:#ffd089"></i>THIS NODE</span>
    </div>
  </div>
</section>

<style>
  .netmap {
    display: flex;
    flex-direction: column;
    border: 1px solid rgba(96, 158, 182, 0.22);
    background: rgba(8, 17, 25, 0.55);
  }
  .netmap-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 8px 10px;
    border-bottom: 1px solid rgba(96, 158, 182, 0.18);
  }
  .netmap-title {
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: 9px;
    letter-spacing: 0.14em;
    color: #5a7f8f;
  }
  .demo-tag {
    margin-left: 8px;
    padding: 1px 5px;
    border: 1px solid rgba(255, 208, 137, 0.5);
    color: #ffd089;
    font-size: 8px;
    letter-spacing: 0.12em;
  }
  .mode-tabs {
    display: flex;
    gap: 2px;
  }
  .mode-tabs button {
    appearance: none;
    border: 1px solid rgba(53, 201, 216, 0.35);
    background: transparent;
    color: #7d929b;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: 9px;
    letter-spacing: 0.14em;
    padding: 3px 10px;
    cursor: pointer;
  }
  .mode-tabs button.active {
    background: rgba(53, 201, 216, 0.18);
    color: #9fe9f2;
  }
  .netmap-body {
    position: relative;
    height: 420px;
  }
  canvas {
    position: absolute;
    inset: 0;
    width: 100%;
    height: 100%;
    display: block;
  }
  .netmap-counts {
    position: absolute;
    left: 8px;
    top: 6px;
    pointer-events: none;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: 9px;
    letter-spacing: 0.12em;
    color: #5a7f8f;
    line-height: 1.65;
  }
  .netmap-legend {
    position: absolute;
    right: 8px;
    bottom: 6px;
    display: flex;
    gap: 12px;
    pointer-events: none;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: 8px;
    letter-spacing: 0.12em;
    color: #5a7f8f;
  }
  .netmap-legend i {
    display: inline-block;
    width: 6px;
    height: 6px;
    margin-right: 4px;
    border-radius: 50%;
  }
</style>

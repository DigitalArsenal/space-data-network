<script lang="ts">
  import { onMount } from 'svelte';
  import {
    GRANTED_DWELL_MS,
    MIN_SEQUENCE_DWELL_MS,
    buildAuthSteps,
    describeAuthFailure,
    extractPeerIdFromKey,
    formatAgentVersionLabel,
    formatFeedsSynced,
    formatFooterNodeLabel,
    formatPeerIdentitySummary,
    formatPeersConnected,
    formatTelemetryCount,
    generateOrbitArcs,
    generateStarfield,
    nodeStepLabels,
    NODE_KEY_NO_PEER_ID_ERROR,
    parseHealthResponse,
    parseNodeInfoResponse,
    parseStatsResponse,
    remainingDwellMs,
    resolvePeerIdentity,
    validateNodeKeyForm,
    type NodeHealthStatus,
    type NodeInfoSnapshot,
    type ResolvedPeerIdentity,
    type StatsSnapshot,
  } from '../lib/login';
  import type { SdnApiClient } from '../../lib/auth/sdn-api-client';

  // Dormant SpaceAware Phase 1A read-only identity lookup. Node-session
  // authentication is intentionally absent until the server-auth-v2 cutover:
  // this document never accepts wallet credentials or signs on the host.
  // `../lib/login.ts` owns the pure display and peer-resolution logic so it
  // stays unit-testable outside the DOM/canvas/network.

  type NetworkStatus = NodeHealthStatus;

  let {
    navigate,
    apiClient,
    showTelemetry = true,
  }: {
    navigate: (path: string) => void;
    apiClient: SdnApiClient;
    showTelemetry?: boolean;
  } = $props();

  const TELEMETRY_REFRESH_MS = 30_000;

  let nodeKey = $state('');
  let phase = $state<'idle' | 'auth' | 'ok'>('idle');
  let nodeStep = $state(-1);
  let err = $state('');
  let reqOpen = $state(false);
  let utcDate = $state('');
  let utcTime = $state('');

  let peerIdentity = $state<ResolvedPeerIdentity | null>(null);

  // Real telemetry (bottom-left panel) + footer identity — fetched once on
  // mount and on a modest interval; fields fail soft to `null`/placeholder
  // dashes (see login.ts's format* helpers) rather than spamming the
  // console on a slow/offline node.
  let stats = $state<StatsSnapshot>({ totalRecords: null, connectedPeers: null, schemaCount: null });
  let nodeInfo = $state<NodeInfoSnapshot>({ peerId: null, agentVersion: null });
  let liveNetworkStatus = $state<NetworkStatus>('NOMINAL');

  let canvasEl: HTMLCanvasElement | undefined;

  const authing = $derived(phase === 'auth' || phase === 'ok');
  const notAuthing = $derived(!authing);
  const granted = $derived(phase === 'ok');

  const netColor = $derived(
    liveNetworkStatus === 'NOMINAL' ? '#5ad6a0' : liveNetworkStatus === 'DEGRADED' ? '#ffb24d' : '#ff5b5b',
  );

  const nodeKeyError = $derived(!!err);

  const authSteps = $derived(buildAuthSteps(nodeStepLabels(), nodeStep));

  const grantedText = $derived(
    peerIdentity
      ? `PEER VERIFIED · ${formatPeerIdentitySummary(peerIdentity)}`
      : 'PEER RESOLVED · LOADING PUBLIC VIEW',
  );

  const trackedDisplay = $derived(formatTelemetryCount(stats.totalRecords));
  const peersDisplay = $derived(formatPeersConnected(stats.connectedPeers));
  const feedsDisplay = $derived(formatFeedsSynced(stats.schemaCount));
  const footerNodeLabel = $derived(formatFooterNodeLabel(nodeInfo.peerId));
  const footerVersionLabel = $derived(formatAgentVersionLabel(nodeInfo.agentVersion));

  let timers: ReturnType<typeof setTimeout>[] = [];
  let telemetryHandle: ReturnType<typeof setInterval> | undefined;

  function sleep(ms: number): Promise<void> {
    return new Promise((resolve) => {
      timers.push(setTimeout(resolve, ms));
    });
  }

  /** Waits out whatever's left of `minMs` since `startedAt` so a fast real sequence doesn't flash by unreadably. */
  async function dwell(startedAt: number, minMs: number): Promise<void> {
    const remaining = remainingDwellMs(performance.now() - startedAt, minMs);
    if (remaining > 0) await sleep(remaining);
  }

  function onNodeKeyInput(event: Event) {
    nodeKey = (event.currentTarget as HTMLTextAreaElement).value;
    err = '';
  }

  function toggleRequestAccess() {
    reqOpen = !reqOpen;
  }

  function drawStarfield() {
    const cv = canvasEl;
    if (!cv) return;
    const dpr = window.devicePixelRatio || 1;
    const w = cv.clientWidth;
    const h = cv.clientHeight;
    if (!w || !h) return;
    cv.width = w * dpr;
    cv.height = h * dpr;
    const ctx = cv.getContext('2d');
    if (!ctx) return;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, w, h);

    for (const star of generateStarfield(w, h)) {
      ctx.fillStyle = `rgba(${star.color},${star.alpha})`;
      ctx.beginPath();
      ctx.arc(star.x, star.y, star.r, 0, Math.PI * 2);
      ctx.fill();
    }

    for (const arc of generateOrbitArcs(w, h)) {
      ctx.beginPath();
      ctx.ellipse(arc.cx, arc.cy, arc.rx, arc.ry, 0, arc.startAngle, arc.endAngle);
      ctx.strokeStyle = arc.strokeStyle;
      ctx.lineWidth = arc.lineWidth;
      ctx.setLineDash(arc.dash);
      ctx.stroke();
    }
    ctx.setLineDash([]);
  }

  /**
   * Real node-key resolve (U1.2, D2 v1): resolves the peer ID (or a
   * multiaddr's trailing `/p2p/<id>`) via `GET /api/v1/peers/{peerId}`
   * through the typed client, surfaces its EPM identity, then enters a
   * read-only public view. No server session is created.
   */
  async function runNodeResolve() {
    phase = 'auth';
    nodeStep = 0;
    peerIdentity = null;
    err = '';
    const startedAt = performance.now();
    try {
      const peerId = extractPeerIdFromKey(nodeKey);
      if (!peerId) throw new Error(NODE_KEY_NO_PEER_ID_ERROR);
      nodeStep = 1;
      const result = await apiClient.requestJson<unknown>(`/peers/${encodeURIComponent(peerId)}`);
      peerIdentity = resolvePeerIdentity(peerId, result.data);
      nodeStep = 2;
      await dwell(startedAt, MIN_SEQUENCE_DWELL_MS);
      phase = 'ok';
      await sleep(GRANTED_DWELL_MS);
      navigate('/orbital');
    } catch (e) {
      phase = 'idle';
      nodeStep = -1;
      err = describeAuthFailure(e);
    }
  }

  function onSubmitNode(event: SubmitEvent) {
    event.preventDefault();
    const validation = validateNodeKeyForm(nodeKey);
    if (validation) {
      err = validation;
      return;
    }
    void runNodeResolve();
  }

  /** Fetches TRACKED/PEERS/FEEDS, the network chip status, and the footer node identity. Fails soft per-field — never throws, never logs. */
  async function refreshTelemetry() {
    try {
      const result = await apiClient.requestJson<unknown>('/stats');
      stats = parseStatsResponse(result.data);
    } catch {
      // Leave the previous snapshot (or placeholder dashes) in place.
    }

    try {
      const result = await apiClient.requestJson<unknown>('/data/health');
      liveNetworkStatus = parseHealthResponse(result.data);
    } catch {
      liveNetworkStatus = 'ALERT';
    }

    try {
      const result = await apiClient.requestJson<unknown>('/api/node/info', { base: 'root' });
      nodeInfo = parseNodeInfoResponse(result.data);
    } catch {
      // Leave the previous snapshot (or placeholder dashes) in place.
    }
  }

  function onExploreCatalog(event: MouseEvent) {
    event.preventDefault();
    navigate('/orbital');
  }

  onMount(() => {
    const tick = () => {
      const iso = new Date().toISOString();
      utcDate = iso.slice(0, 10);
      utcTime = iso.slice(11, 19);
    };
    tick();
    const clockHandle = setInterval(tick, 1000);

    drawStarfield();
    const onResize = () => drawStarfield();
    window.addEventListener('resize', onResize);

    void refreshTelemetry();
    telemetryHandle = setInterval(() => void refreshTelemetry(), TELEMETRY_REFRESH_MS);

    return () => {
      clearInterval(clockHandle);
      window.removeEventListener('resize', onResize);
      if (telemetryHandle) clearInterval(telemetryHandle);
      timers.forEach((t) => clearTimeout(t));
      timers = [];
    };
  });
</script>

<div class="sa-login" data-screen-label="Login">
  <canvas bind:this={canvasEl} class="sa-login-canvas"></canvas>
  <div class="sa-login-glow"></div>
  <div class="sa-login-vignette"></div>

  <div class="sa-login-network-chip" title={`Network status: ${liveNetworkStatus}`}>
    <span class="sa-login-network-dot" style={`background:${netColor};box-shadow:0 0 7px ${netColor};`}
    ></span>
    <span class="sa-login-network-label"
      >NETWORK <span style={`color:${netColor};`}>{liveNetworkStatus}</span></span
    >
  </div>

  <div class="sa-login-center">
    <div class="sa-login-stack">
      <div class="sa-login-brand">
        <svg width="46" height="46" viewBox="0 0 48 48" fill="none" class="sa-login-brand-svg">
          <circle cx="24" cy="24" r="9.5" stroke="#35c9d8" stroke-width="1.5" />
          <ellipse
            cx="24"
            cy="24"
            rx="20"
            ry="7.5"
            stroke="#7fb4d6"
            stroke-width="1.1"
            transform="rotate(-18 24 24)"
          />
          <circle cx="42.2" cy="17.6" r="2" fill="#9fe9f2" />
        </svg>
        <div class="sa-login-brand-text">
          <div class="sa-login-wordmark">SPACE DATA NETWORK</div>
          <div class="sa-login-kicker">SDN NODE · PUBLIC IDENTITY LOOKUP</div>
        </div>
      </div>

      <div class="sa-login-panel">
        <div class="sa-login-section-title">NODE KEY · READ-ONLY</div>

        <div class="sa-login-body">
          {#if notAuthing}
            <form class="sa-login-form" onsubmit={onSubmitNode}>
              <label class="sa-login-label">
                <span class="sa-login-label-text">PEER ID / MULTIADDR</span>
                <textarea
                  id="sa-login-node-key"
                  name="node-key"
                  class="sa-login-textarea"
                  class:is-error={nodeKeyError}
                  rows="3"
                  value={nodeKey}
                  oninput={onNodeKeyInput}
                  placeholder="16Uiu2HAm… peer ID, or /ip4/…/tcp/4001 multiaddr"
                  spellcheck="false"
                  title="Peer ID or multiaddr"
                ></textarea>
              </label>
              <div class="sa-login-hint">
                <span class="sa-login-hint-icon">◈</span>IDENTITY IS VERIFIED AGAINST THE EPM REGISTRY · ED25519
              </div>
              {#if err}
                <div class="sa-login-error">
                  <span class="sa-login-error-icon">⚠</span>
                  <span class="sa-login-error-text">{err}</span>
                </div>
              {/if}
              <button
                type="submit"
                class="sa-login-submit"
                title="Resolve peer identity and open the public orbital view"
              >
                <span class="sa-login-submit-icon">◈</span>RESOLVE NODE IDENTITY
              </button>
            </form>
          {:else if authing}
            <div class="sa-login-auth">
              {#each authSteps as s, i (i)}
                <div class="sa-login-step">
                  <span class="sa-login-step-glyph" style={`color:${s.color};animation:${s.anim};`}
                    >{s.glyph}</span
                  >
                  <span class="sa-login-step-label" style={`color:${s.labelColor};`}>{s.label}</span>
                  <span class="sa-login-step-status" style={`color:${s.color};`}>{s.status}</span>
                </div>
              {/each}
              {#if granted}
                <div class="sa-login-granted">
                  <span class="sa-login-granted-dot"></span>
                  <span class="sa-login-granted-text">{grantedText}</span>
                </div>
              {/if}
            </div>
          {/if}

          {#if notAuthing}
            <div class="sa-login-links">
              <div class="sa-login-links-row">
                <button
                  type="button"
                  class="sa-login-link-btn"
                  onclick={toggleRequestAccess}
                  title="Request node access from an administrator"
                >
                  REQUEST ACCESS
                </button>
              </div>
              {#if reqOpen}
                <div class="sa-login-info-note">
                  PROVISIONING IS HANDLED BY YOUR NODE ADMINISTRATOR VIA THE EPM REGISTRY. SIGNED REQUESTS
                  ONLY.
                </div>
              {/if}
              <div class="sa-login-divider"></div>
              <a
                href="/orbital"
                class="sa-login-explore"
                title="Browse the public orbital view"
                onclick={onExploreCatalog}
              >
                <span class="sa-login-explore-icon">◯</span>EXPLORE PUBLIC VIEW · READ-ONLY
              </a>
            </div>
          {/if}
        </div>
      </div>
    </div>
  </div>

  {#if showTelemetry}
    <div class="sa-login-telemetry">
      <div class="sa-login-telemetry-title">NODE TELEMETRY</div>
      <div class="sa-login-telemetry-row">
        <span>TRACKED</span><span class="sa-login-telemetry-value">{trackedDisplay}</span>
      </div>
      <div class="sa-login-telemetry-row">
        <span>PEERS</span><span class="sa-login-telemetry-value sa-login-telemetry-value--cyan"
          >{peersDisplay}</span
        >
      </div>
      <div class="sa-login-telemetry-row">
        <span>FEEDS</span><span class="sa-login-telemetry-value">{feedsDisplay}</span>
      </div>
      <!--
        SIM LINK (U1.1 mock: "BASILISK ✓") is DROPPED, not demo-badged: a
        stock sdn-server node has no basilisk feed to report on, and there is
        no real data source for this row (see U1.2 task notes). The other 3
        telemetry rows keep their existing layout/spacing unchanged.
      -->
    </div>
  {/if}

  <div class="sa-login-footer">
    <span class="sa-login-footer-version">SPACE DATA NETWORK · {footerVersionLabel}</span>
    <span class="sa-login-footer-node">{footerNodeLabel}</span>
    <span class="sa-login-footer-clock"
      >{utcDate} <span class="sa-login-footer-time">{utcTime}</span> UTC<span class="sa-login-footer-caret"
        >▍</span
      ></span
    >
  </div>
</div>

<style>
  .sa-login {
    position: fixed;
    inset: 0;
    background: #04060a;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    color: #c7d6dd;
    overflow: hidden;
    -webkit-font-smoothing: antialiased;
  }

  .sa-login-canvas {
    position: absolute;
    inset: 0;
    width: 100%;
    height: 100%;
    display: block;
  }

  .sa-login-glow {
    position: absolute;
    inset: 0;
    pointer-events: none;
    background: radial-gradient(ellipse 60% 46% at 50% 42%, rgba(53, 201, 216, 0.06), transparent 60%);
  }

  .sa-login-vignette {
    position: absolute;
    inset: 0;
    pointer-events: none;
    box-shadow: inset 0 0 220px 50px rgba(0, 0, 0, 0.78);
  }

  /* ---- network status chip (top-right) ---- */

  .sa-login-network-chip {
    position: absolute;
    top: 16px;
    right: 16px;
    z-index: 6;
    display: flex;
    align-items: center;
    gap: 8px;
    background: rgba(7, 12, 18, 0.78);
    border: 1px solid rgba(110, 170, 190, 0.2);
    backdrop-filter: blur(6px);
    -webkit-backdrop-filter: blur(6px);
    padding: 6px 11px;
  }

  .sa-login-network-dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    animation: sa-pulse 1.6s ease-in-out infinite;
  }

  .sa-login-network-label {
    font-size: 9.5px;
    letter-spacing: 0.18em;
    color: #9fb3bc;
  }

  /* ---- center column ---- */

  .sa-login-center {
    position: absolute;
    inset: 0;
    z-index: 5;
    display: flex;
    flex-direction: column;
    overflow-y: auto;
    padding: 24px 16px 56px;
  }

  .sa-login-stack {
    margin: auto;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 24px;
  }

  /* ---- brand ---- */

  .sa-login-brand {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 12px;
  }

  .sa-login-brand-svg {
    display: block;
    filter: drop-shadow(0 0 8px rgba(53, 201, 216, 0.45));
  }

  .sa-login-brand-text {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 6px;
  }

  .sa-login-wordmark {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 700;
    font-size: 25px;
    letter-spacing: 0.22em;
    color: #eaf6f8;
    text-shadow: 0 0 18px rgba(53, 201, 216, 0.35);
  }

  .sa-login-kicker {
    font-size: 10px;
    letter-spacing: 0.3em;
    color: #5a7a8a;
  }

  /* ---- panel ---- */

  .sa-login-panel {
    width: 392px;
    max-width: 100%;
    background: rgba(7, 12, 18, 0.84);
    border: 1px solid rgba(110, 170, 190, 0.22);
    border-top: 2px solid rgba(53, 201, 216, 0.6);
    backdrop-filter: blur(8px);
    -webkit-backdrop-filter: blur(8px);
    box-shadow: 0 18px 50px rgba(0, 0, 0, 0.6);
  }

  .sa-login-section-title {
    border-bottom: 1px solid rgba(110, 170, 190, 0.16);
    color: #eaf6f8;
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 600;
    font-size: 11px;
    letter-spacing: 0.14em;
    padding: 11px 26px 9px;
  }

  .sa-login-body {
    padding: 22px 26px 20px;
  }

  /* ---- forms ---- */

  .sa-login-form {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }

  .sa-login-label {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }

  .sa-login-label-text {
    font-size: 9.5px;
    letter-spacing: 0.22em;
    color: #5a7a8a;
  }

  .sa-login-textarea {
    width: 100%;
    background: rgba(4, 8, 12, 0.65);
    border: 1px solid rgba(110, 170, 190, 0.28);
    color: #eaf6f8;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    outline: none;
  }

  .sa-login-textarea {
    font-size: 11.5px;
    line-height: 1.6;
    padding: 10px 12px;
    resize: none;
  }

  .sa-login-textarea.is-error {
    border-color: rgba(255, 107, 107, 0.55);
  }

  .sa-login-textarea:focus {
    border-color: #35c9d8;
    box-shadow: 0 0 0 1px rgba(53, 201, 216, 0.3);
  }

  .sa-login-textarea::placeholder {
    color: #44586a;
    opacity: 1;
  }

  .sa-login-hint {
    display: flex;
    align-items: center;
    gap: 7px;
    font-size: 9.5px;
    letter-spacing: 0.08em;
    color: #5a7a8a;
  }

  .sa-login-hint-icon {
    font-size: 12px;
    color: #7fb4d6;
  }

  .sa-login-error {
    display: flex;
    align-items: center;
    gap: 7px;
    border: 1px solid rgba(255, 107, 107, 0.35);
    background: rgba(255, 107, 107, 0.07);
    padding: 7px 10px;
  }

  .sa-login-error-icon {
    font-size: 12px;
    color: #ff8d8d;
  }

  .sa-login-error-text {
    font-size: 10px;
    letter-spacing: 0.1em;
    color: #ff9b9b;
  }

  .sa-login-submit {
    width: 100%;
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 9px;
    background: rgba(53, 201, 216, 0.14);
    border: 1px solid rgba(53, 201, 216, 0.5);
    color: #9fe9f2;
    cursor: pointer;
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 700;
    font-size: 13px;
    letter-spacing: 0.16em;
    padding: 11px 0;
  }

  .sa-login-submit:hover {
    background: rgba(53, 201, 216, 0.26);
    color: #eaf6f8;
  }

  .sa-login-submit-icon {
    font-size: 14px;
    line-height: 1;
  }

  /* ---- auth progress ---- */

  .sa-login-auth {
    display: flex;
    flex-direction: column;
    gap: 13px;
    padding: 6px 0 2px;
  }

  .sa-login-step {
    display: flex;
    align-items: center;
    gap: 11px;
  }

  .sa-login-step-glyph {
    width: 16px;
    flex: none;
    display: flex;
    align-items: center;
    justify-content: center;
    font-size: 12px;
  }

  .sa-login-step-label {
    flex: 1;
    font-size: 11px;
    letter-spacing: 0.12em;
  }

  .sa-login-step-status {
    font-size: 9px;
    letter-spacing: 0.14em;
  }

  .sa-login-granted {
    display: flex;
    align-items: center;
    gap: 8px;
    border: 1px solid rgba(90, 214, 160, 0.4);
    background: rgba(90, 214, 160, 0.07);
    padding: 8px 11px;
    margin-top: 2px;
  }

  .sa-login-granted-dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: #5ad6a0;
    box-shadow: 0 0 7px #5ad6a0;
  }

  .sa-login-granted-text {
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 700;
    font-size: 10.5px;
    letter-spacing: 0.14em;
    color: #5ad6a0;
  }

  /* ---- secondary links ---- */

  .sa-login-links {
    display: flex;
    flex-direction: column;
    gap: 12px;
    margin-top: 16px;
  }

  .sa-login-links-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
  }

  .sa-login-link-btn {
    background: transparent;
    border: none;
    padding: 0;
    cursor: pointer;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: 9.5px;
    letter-spacing: 0.14em;
    color: #7fb4d6;
  }

  .sa-login-link-btn:hover {
    color: #9fe9f2;
  }

  .sa-login-info-note {
    font-size: 9.5px;
    line-height: 1.7;
    letter-spacing: 0.06em;
    color: #5a7a8a;
    border: 1px solid rgba(110, 170, 190, 0.16);
    background: rgba(110, 170, 190, 0.04);
    padding: 8px 10px;
  }

  .sa-login-divider {
    height: 1px;
    background: rgba(110, 170, 190, 0.14);
  }

  .sa-login-explore {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 8px;
    text-decoration: none;
    border: 1px solid rgba(110, 170, 190, 0.22);
    background: rgba(7, 12, 18, 0.5);
    padding: 8px 0;
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 600;
    font-size: 10.5px;
    letter-spacing: 0.14em;
    color: #9fb3bc;
    cursor: pointer;
  }

  .sa-login-explore:hover {
    border-color: rgba(120, 190, 230, 0.45);
    color: #cfe3ec;
  }

  .sa-login-explore-icon {
    font-size: 12.5px;
    line-height: 1;
    color: #7fb4d6;
  }

  /* ---- telemetry panel (bottom-left) ---- */

  .sa-login-telemetry {
    position: absolute;
    left: 16px;
    bottom: 46px;
    z-index: 6;
    width: 198px;
    background: rgba(7, 12, 18, 0.72);
    border: 1px solid rgba(110, 170, 190, 0.16);
    backdrop-filter: blur(6px);
    -webkit-backdrop-filter: blur(6px);
    padding: 9px 11px;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }

  .sa-login-telemetry-title {
    font-size: 9px;
    letter-spacing: 0.22em;
    color: #5a7a8a;
    margin-bottom: 2px;
  }

  .sa-login-telemetry-row {
    display: flex;
    justify-content: space-between;
    font-size: 10.5px;
    color: #9fb3bc;
  }

  .sa-login-telemetry-value {
    color: #eaf6f8;
  }

  .sa-login-telemetry-value--cyan {
    color: #35c9d8;
  }

  /* .sa-login-telemetry-value--green (U1.1: SIM LINK "BASILISK ✓") removed
     with the SIM LINK row — see the telemetry panel markup above. */

  /* ---- footer strip ---- */

  .sa-login-footer {
    position: absolute;
    left: 0;
    right: 0;
    bottom: 0;
    z-index: 6;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 14px;
    background: rgba(6, 10, 15, 0.85);
    border-top: 1px solid rgba(110, 170, 190, 0.14);
    backdrop-filter: blur(6px);
    -webkit-backdrop-filter: blur(6px);
    padding: 7px 16px;
  }

  .sa-login-footer-version {
    font-size: 9px;
    letter-spacing: 0.16em;
    color: #5a7a8a;
  }

  .sa-login-footer-node {
    font-size: 9px;
    letter-spacing: 0.12em;
    color: #44586a;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .sa-login-footer-clock {
    font-size: 9px;
    letter-spacing: 0.16em;
    color: #7d929b;
    white-space: nowrap;
  }

  .sa-login-footer-time {
    color: #9fd4f5;
  }

  .sa-login-footer-caret {
    display: inline-block;
    width: 6px;
    color: #35c9d8;
    animation: sa-blink 1.2s step-end infinite;
  }
</style>

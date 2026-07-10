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
    operatorStepIndexForStage,
    operatorStepLabels,
    parseHealthResponse,
    parseNodeInfoResponse,
    parseStatsResponse,
    remainingDwellMs,
    resolvePeerIdentity,
    validateNodeKeyForm,
    validateOperatorForm,
    type NodeHealthStatus,
    type NodeInfoSnapshot,
    type ResolvedPeerIdentity,
    type StatsSnapshot,
  } from '../lib/login';
  import type { AuthSessionState, AuthStore } from '../../lib/auth/auth-store';
  import { unlockLocalWallet, type UnlockedWallet } from '../../lib/auth/local-wallet';
  import type { SdnApiClient } from '../../lib/auth/sdn-api-client';

  // U1.1 pixel port of login/Login.dc.html; U1.2 wires it to the real U0.3
  // auth/data surface. Ground truth for pixels: the .dc.html inline
  // styles/markup/script + its README.md spec — behavior now comes from
  // `authStore`/`apiClient` instead of the old fixed-timer mock sequence.
  // `../lib/login.ts` owns all pure logic (PRNG, validation, step view
  // models, real-stage mapping, telemetry/peer-identity parsing) so it stays
  // unit-testable outside the DOM/canvas/network.

  type AuthTab = 'operator' | 'node';
  type NetworkStatus = NodeHealthStatus;

  let {
    navigate,
    authStore,
    authState,
    apiClient,
    defaultTab = 'operator',
    showTelemetry = true,
  }: {
    navigate: (path: string) => void;
    authStore: AuthStore;
    authState: AuthSessionState;
    apiClient: SdnApiClient;
    defaultTab?: AuthTab;
    showTelemetry?: boolean;
  } = $props();

  const REMEMBERED_OPERATOR_KEY = 'sa_remembered_operator';
  const TELEMETRY_REFRESH_MS = 30_000;

  let tab = $state<AuthTab>(defaultTab);
  let opId = $state('');
  let pass = $state('');
  let nodeKey = $state('');
  let remember = $state(false);
  let phase = $state<'idle' | 'auth' | 'ok'>('idle');
  let operatorStep = $state(-1);
  let nodeStep = $state(-1);
  let err = $state('');
  let reqOpen = $state(false);
  let utcDate = $state('');
  let utcTime = $state('');

  // Set only while awaiting `authStore.loginWithWallet(...)`, so a stale
  // `authState.stage` left over from a PRIOR session (e.g. the user
  // navigated back to /login while already authenticated) can never be
  // misread as progress on an attempt that hasn't started yet.
  let watchingAuthState = $state(false);

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
  const showOperatorForm = $derived(tab === 'operator' && !authing);
  const showNodeForm = $derived(tab === 'node' && !authing);

  const netColor = $derived(
    liveNetworkStatus === 'NOMINAL' ? '#5ad6a0' : liveNetworkStatus === 'DEGRADED' ? '#ffb24d' : '#ff5b5b',
  );

  const opIdError = $derived(!!err && !opId.trim());
  const passError = $derived(!!err && !pass);
  const nodeKeyError = $derived(!!err && tab === 'node');

  const authSteps = $derived(
    buildAuthSteps(tab === 'operator' ? operatorStepLabels() : nodeStepLabels(), tab === 'operator' ? operatorStep : nodeStep),
  );

  const grantedText = $derived(
    tab === 'operator'
      ? 'ACCESS GRANTED · LOADING SDN CONSOLE'
      : peerIdentity
        ? `PEER VERIFIED · ${formatPeerIdentitySummary(peerIdentity)}`
        : 'PEER RESOLVED · LOADING CATALOG',
  );

  const trackedDisplay = $derived(formatTelemetryCount(stats.totalRecords));
  const peersDisplay = $derived(formatPeersConnected(stats.connectedPeers));
  const feedsDisplay = $derived(formatFeedsSynced(stats.schemaCount));
  const footerNodeLabel = $derived(formatFooterNodeLabel(nodeInfo.peerId));
  const footerVersionLabel = $derived(formatAgentVersionLabel(nodeInfo.agentVersion));

  let timers: ReturnType<typeof setTimeout>[] = [];
  let telemetryHandle: ReturnType<typeof setInterval> | undefined;

  // Reflects the real `authStore` stage onto the operator tab's step rows
  // while (and only while) we're awaiting our own `loginWithWallet` call —
  // see `watchingAuthState` above.
  $effect(() => {
    if (!watchingAuthState) return;
    const target = operatorStepIndexForStage(authState.stage);
    if (target > operatorStep) operatorStep = target;
  });

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

  function persistRememberedOperator() {
    try {
      if (remember) {
        localStorage.setItem(REMEMBERED_OPERATOR_KEY, opId);
      } else {
        localStorage.removeItem(REMEMBERED_OPERATOR_KEY);
      }
    } catch {
      // localStorage unavailable (private mode, etc.) — non-fatal.
    }
  }

  function selectTab(next: AuthTab) {
    tab = next;
    err = '';
  }

  function onOpIdInput(event: Event) {
    opId = (event.currentTarget as HTMLInputElement).value;
    err = '';
  }

  function onPassInput(event: Event) {
    pass = (event.currentTarget as HTMLInputElement).value;
    err = '';
  }

  function onNodeKeyInput(event: Event) {
    nodeKey = (event.currentTarget as HTMLTextAreaElement).value;
    err = '';
  }

  function toggleRemember() {
    remember = !remember;
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
   * Real operator auth (U1.2): local wallet unlock (D1 — "operator ID"
   * labels a browser-side wallet, "passphrase" decrypts it) →
   * `authStore.loginWithWallet` (challenge fetch → sign → verify →
   * `auth/me` hydration, all against the shared U0.3 store so
   * `SpaceAwareApp`'s route guard sees the resulting session). Step rows
   * advance live off `authState.stage` via the `$effect` above; on any
   * failure the sequence aborts, the form comes back, and the REAL error
   * (local-wallet or `SdnApiError` message) lands in the existing banner.
   */
  async function runOperatorAuth() {
    phase = 'auth';
    operatorStep = 0;
    err = '';
    const startedAt = performance.now();
    let wallet: UnlockedWallet | null = null;
    try {
      wallet = await unlockLocalWallet(opId, pass);
      watchingAuthState = true;
      try {
        await authStore.loginWithWallet(wallet);
      } finally {
        watchingAuthState = false;
      }
      operatorStep = 2;
      await dwell(startedAt, MIN_SEQUENCE_DWELL_MS);
      persistRememberedOperator();
      phase = 'ok';
      await sleep(GRANTED_DWELL_MS);
      navigate('/console');
    } catch (e) {
      phase = 'idle';
      operatorStep = -1;
      err = describeAuthFailure(e);
    } finally {
      wallet?.lock();
    }
  }

  /**
   * Real node-key resolve (U1.2, D2 v1): resolves the peer ID (or a
   * multiaddr's trailing `/p2p/<id>`) via `GET /api/v1/peers/{peerId}`
   * through the typed client, surfaces its EPM identity, then enters a
   * read-only explore of the public catalog — no session is created (D2:
   * only `/console` requires auth), so this never touches `authStore`.
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

  function onSubmitOperator(event: SubmitEvent) {
    event.preventDefault();
    const validation = validateOperatorForm(opId, pass);
    if (validation) {
      err = validation;
      return;
    }
    void runOperatorAuth();
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
    try {
      const saved = localStorage.getItem(REMEMBERED_OPERATOR_KEY);
      if (saved) {
        opId = saved;
        remember = true;
      }
    } catch {
      // localStorage unavailable — leave fields at defaults.
    }

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
          <div class="sa-login-kicker">SDN NODE · SECURE ACCESS</div>
        </div>
      </div>

      <div class="sa-login-panel">
        <div class="sa-login-tabs">
          <button
            type="button"
            class="sa-login-tab"
            class:is-active={tab === 'operator'}
            title="Sign in with operator credentials"
            onclick={() => selectTab('operator')}
          >
            OPERATOR
          </button>
          <button
            type="button"
            class="sa-login-tab"
            class:is-active={tab === 'node'}
            title="Authenticate with a node peer key"
            onclick={() => selectTab('node')}
          >
            NODE KEY
          </button>
        </div>

        <div class="sa-login-body">
          {#if showOperatorForm}
            <form class="sa-login-form" onsubmit={onSubmitOperator}>
              <label class="sa-login-label">
                <span class="sa-login-label-text">OPERATOR ID</span>
                <input
                  type="text"
                  id="sa-login-operator-id"
                  name="operator-id"
                  class="sa-login-input"
                  class:is-error={opIdError}
                  value={opId}
                  oninput={onOpIdInput}
                  placeholder="operator@spacedatanetwork.io"
                  autocomplete="username"
                  spellcheck="false"
                  title="Operator ID"
                />
              </label>
              <label class="sa-login-label">
                <span class="sa-login-label-text">PASSPHRASE</span>
                <input
                  type="password"
                  id="sa-login-passphrase"
                  name="passphrase"
                  class="sa-login-input"
                  class:is-error={passError}
                  value={pass}
                  oninput={onPassInput}
                  placeholder="••••••••••••"
                  autocomplete="current-password"
                  title="Passphrase"
                />
              </label>
              <button
                type="button"
                class="sa-login-remember"
                onclick={toggleRemember}
                title="Keep operator ID on this terminal"
              >
                <span class="sa-login-remember-box" class:is-checked={remember}
                  >{remember ? '✓' : ''}</span
                >
                <span class="sa-login-remember-text">REMEMBER OPERATOR ID</span>
              </button>
              {#if err}
                <div class="sa-login-error">
                  <span class="sa-login-error-icon">⚠</span>
                  <span class="sa-login-error-text">{err}</span>
                </div>
              {/if}
              <button type="submit" class="sa-login-submit" title="Authenticate and open the SDN console">
                <span class="sa-login-submit-icon">◈</span>SIGN IN
              </button>
            </form>
          {:else if showNodeForm}
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
                title="Verify peer identity and open the SDN console"
              >
                <span class="sa-login-submit-icon">◈</span>AUTHENTICATE NODE
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
                <button
                  type="button"
                  class="sa-login-link-btn"
                  onclick={toggleRequestAccess}
                  title="Recover a lost operator passphrase"
                >
                  RECOVER PASSPHRASE
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
                title="Browse the public catalog without signing in"
                onclick={onExploreCatalog}
              >
                <span class="sa-login-explore-icon">◯</span>EXPLORE PUBLIC CATALOG · READ-ONLY
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

  .sa-login-tabs {
    display: flex;
    border-bottom: 1px solid rgba(110, 170, 190, 0.16);
  }

  .sa-login-tab {
    flex: 1;
    background: transparent;
    border: none;
    border-bottom: 2px solid transparent;
    color: #5a7a8a;
    cursor: pointer;
    font-family: 'Chakra Petch', ui-monospace, monospace;
    font-weight: 600;
    font-size: 12px;
    letter-spacing: 0.14em;
    padding: 11px 0 9px;
  }

  .sa-login-tab.is-active {
    border-bottom-color: #35c9d8;
    color: #eaf6f8;
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

  .sa-login-input,
  .sa-login-textarea {
    width: 100%;
    background: rgba(4, 8, 12, 0.65);
    border: 1px solid rgba(110, 170, 190, 0.28);
    color: #eaf6f8;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    outline: none;
  }

  .sa-login-input {
    font-size: 13px;
    padding: 10px 12px;
  }

  .sa-login-textarea {
    font-size: 11.5px;
    line-height: 1.6;
    padding: 10px 12px;
    resize: none;
  }

  .sa-login-input.is-error,
  .sa-login-textarea.is-error {
    border-color: rgba(255, 107, 107, 0.55);
  }

  .sa-login-input:focus,
  .sa-login-textarea:focus {
    border-color: #35c9d8;
    box-shadow: 0 0 0 1px rgba(53, 201, 216, 0.3);
  }

  .sa-login-input::placeholder,
  .sa-login-textarea::placeholder {
    color: #44586a;
    opacity: 1;
  }

  .sa-login-input:-webkit-autofill,
  .sa-login-input:-webkit-autofill:focus {
    -webkit-text-fill-color: #eaf6f8;
    -webkit-box-shadow: 0 0 0 40px #0a1218 inset;
    caret-color: #eaf6f8;
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

  .sa-login-remember {
    display: flex;
    align-items: center;
    gap: 9px;
    background: transparent;
    border: none;
    padding: 0;
    cursor: pointer;
    text-align: left;
    width: fit-content;
  }

  .sa-login-remember-box {
    width: 14px;
    height: 14px;
    flex: none;
    display: flex;
    align-items: center;
    justify-content: center;
    border: 1px solid rgba(110, 170, 190, 0.4);
    background: transparent;
    color: #04060a;
    font-size: 10px;
    line-height: 1;
    font-weight: 700;
  }

  .sa-login-remember-box.is-checked {
    border-color: #35c9d8;
    background: #35c9d8;
  }

  .sa-login-remember-text {
    font-size: 10px;
    letter-spacing: 0.14em;
    color: #7d929b;
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

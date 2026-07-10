<script lang="ts">
  import { onMount } from 'svelte';
  import {
    AUTH_STEP_TIMINGS_MS,
    buildAuthSteps,
    generateOrbitArcs,
    generateStarfield,
    nodeStepLabels,
    operatorStepLabels,
    validateNodeKeyForm,
    validateOperatorForm,
  } from '../lib/login';

  // U1.1 pixel port of login/Login.dc.html (MOCK-STAGED — no real auth; wired
  // in U1.2). Ground truth: the .dc.html inline styles/markup/script + its
  // README.md spec. `../lib/login.ts` owns the pure logic (PRNG, validation,
  // step view models) so it stays unit-testable outside the DOM/canvas.

  type AuthTab = 'operator' | 'node';
  type NetworkStatus = 'NOMINAL' | 'DEGRADED' | 'ALERT';

  let {
    navigate,
    defaultTab = 'operator',
    networkStatus = 'NOMINAL',
    showTelemetry = true,
  }: {
    navigate: (path: string) => void;
    defaultTab?: AuthTab;
    networkStatus?: NetworkStatus;
    showTelemetry?: boolean;
  } = $props();

  const REMEMBERED_OPERATOR_KEY = 'sa_remembered_operator';

  let tab = $state<AuthTab>(defaultTab);
  let opId = $state('');
  let pass = $state('');
  let nodeKey = $state('');
  let remember = $state(false);
  let phase = $state<'idle' | 'auth' | 'ok'>('idle');
  let step = $state(-1);
  let err = $state('');
  let reqOpen = $state(false);
  let utcDate = $state('');
  let utcTime = $state('');

  let canvasEl: HTMLCanvasElement | undefined;

  const authing = $derived(phase === 'auth' || phase === 'ok');
  const notAuthing = $derived(!authing);
  const granted = $derived(phase === 'ok');
  const showOperatorForm = $derived(tab === 'operator' && !authing);
  const showNodeForm = $derived(tab === 'node' && !authing);

  const netColor = $derived(
    networkStatus === 'NOMINAL' ? '#5ad6a0' : networkStatus === 'DEGRADED' ? '#ffb24d' : '#ff5b5b',
  );

  const opIdError = $derived(!!err && !opId.trim());
  const passError = $derived(!!err && !pass);
  const nodeKeyError = $derived(!!err && tab === 'node');

  const authSteps = $derived(
    buildAuthSteps(tab === 'operator' ? operatorStepLabels() : nodeStepLabels(), step),
  );

  let timers: ReturnType<typeof setTimeout>[] = [];

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

  function startAuth(kind: AuthTab) {
    phase = 'auth';
    step = 0;
    err = '';
    const push = (ms: number, fn: () => void) => {
      timers.push(setTimeout(fn, ms));
    };
    push(AUTH_STEP_TIMINGS_MS.step1, () => {
      step = 1;
    });
    push(AUTH_STEP_TIMINGS_MS.step2, () => {
      step = 2;
    });
    push(AUTH_STEP_TIMINGS_MS.complete, () => {
      step = 3;
      phase = 'ok';
      try {
        if (kind === 'operator' && remember) {
          localStorage.setItem(REMEMBERED_OPERATOR_KEY, opId);
        } else if (kind === 'operator' && !remember) {
          localStorage.removeItem(REMEMBERED_OPERATOR_KEY);
        }
      } catch {
        // localStorage unavailable (private mode, etc.) — non-fatal.
      }
    });
    push(AUTH_STEP_TIMINGS_MS.redirect, () => {
      navigate('/console');
    });
  }

  function onSubmitOperator(event: SubmitEvent) {
    event.preventDefault();
    const validation = validateOperatorForm(opId, pass);
    if (validation) {
      err = validation;
      return;
    }
    startAuth('operator');
  }

  function onSubmitNode(event: SubmitEvent) {
    event.preventDefault();
    const validation = validateNodeKeyForm(nodeKey);
    if (validation) {
      err = validation;
      return;
    }
    startAuth('node');
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

    return () => {
      clearInterval(clockHandle);
      window.removeEventListener('resize', onResize);
      timers.forEach((t) => clearTimeout(t));
      timers = [];
    };
  });
</script>

<div class="sa-login" data-screen-label="Login">
  <canvas bind:this={canvasEl} class="sa-login-canvas"></canvas>
  <div class="sa-login-glow"></div>
  <div class="sa-login-vignette"></div>

  <div class="sa-login-network-chip" title={`Network status: ${networkStatus}`}>
    <span class="sa-login-network-dot" style={`background:${netColor};box-shadow:0 0 7px ${netColor};`}
    ></span>
    <span class="sa-login-network-label"
      >NETWORK <span style={`color:${netColor};`}>{networkStatus}</span></span
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
                  <span class="sa-login-granted-text">ACCESS GRANTED · LOADING SDN CONSOLE</span>
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
        <span>TRACKED</span><span class="sa-login-telemetry-value">31,000</span>
      </div>
      <div class="sa-login-telemetry-row">
        <span>PEERS</span><span class="sa-login-telemetry-value sa-login-telemetry-value--cyan"
          >3 CONNECTED</span
        >
      </div>
      <div class="sa-login-telemetry-row">
        <span>FEEDS</span><span class="sa-login-telemetry-value">9 SYNCED</span>
      </div>
      <div class="sa-login-telemetry-row">
        <span>SIM LINK</span><span class="sa-login-telemetry-value sa-login-telemetry-value--green"
          >BASILISK ✓</span
        >
      </div>
    </div>
  {/if}

  <div class="sa-login-footer">
    <span class="sa-login-footer-version">SPACE DATA NETWORK · NODE v1.4.2</span>
    <span class="sa-login-footer-node">THIS NODE · 16Uiu2HAm1Lbvwj…Z5Fm45 · COLORADO SPRINGS, US</span>
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

  .sa-login-telemetry-value--green {
    color: #5ad6a0;
  }

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

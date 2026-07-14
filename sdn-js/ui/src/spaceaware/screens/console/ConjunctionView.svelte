<script lang="ts">
  /**
   * CONJUNCTION console view (loop task U3.9). Ground truth: the
   * `<!-- ============ CONJUNCTION ============ -->` block in
   * `design_handoff/sdn_console/SDN Console.dc.html` — see
   * `ConjunctionTaskPanel.svelte`/`ConjunctionResultsPanel.svelte`/
   * `ConjunctionProvenancePanel.svelte` (split out per the loop task's own
   * "split subcomponents if huge" allowance) for the pixel-level styling
   * port of the three panels this view assembles. The shared `ConsoleHeader`
   * already renders "CONJUNCTION · PRIVATE SCREENING" (`lib/console.ts`), so
   * this view starts at the CONFIGURE panel.
   *
   * All data wiring/view-model building lives in `../../lib/conjunction-data.ts`
   * — see that file's doc comment for decision D4's honesty split (SCREEN
   * TARGET + ① DATA SOURCES are real; ② PROPAGATOR/③ CRITERIA are honest
   * unused client-side state; the LIVE card/RESULTS/PROVENANCE panels are
   * DEMO-tagged fabricated data, since no conjunction-screening engine is
   * wired to this build). This component owns every piece of UI state
   * (source precedence order/on-off, propagator selection, criteria values,
   * results display mode, the live-stream ticker, the one-off popover) and
   * threads it down as props — the three panel components render only.
   *
   * SCREEN TARGET reuses `groups-data.ts`'s shared `sdn_shared_groups` store
   * (same one GROUPS/the 3D console read) so target pills and the status
   * strip are never a second, divergent copy of group data. This view also
   * consumes an optional `?group=` deep link on mount via
   * `console.ts`'s `parseConsoleDeepLinkQuery` + `groups-data.ts`'s
   * `resolveDeepLinkGroupId` — mirroring `GroupsView.svelte`'s own mount
   * logic — even though GROUPS' own "SCREEN FOR CONJUNCTIONS"/"MONITOR
   * CONJUNCTIONS" buttons don't pass one today (`GROUPS_CONJUNCTION_CONSOLE_PATH`
   * is a bare `/console/conjunction`, out of this task's lock scope to
   * change); a manual or future deep link still gets honored for free.
   *
   * The live-stream ticker (`streamTick`, mock's `setInterval(...,2600)`)
   * runs for the lifetime of this component and is cleared on unmount —
   * see `conjunction-data.ts`'s `CONJUNCTION_STREAM_TICK_INTERVAL_MS`/
   * `nextStreamTick`.
   */
  import { onMount } from 'svelte';
  import ConjunctionTaskPanel from './ConjunctionTaskPanel.svelte';
  import ConjunctionResultsPanel from './ConjunctionResultsPanel.svelte';
  import ConjunctionProvenancePanel from './ConjunctionProvenancePanel.svelte';
  import { parseConsoleDeepLinkQuery } from '../../lib/console';
  import {
    loadSharedGroups,
    resolveDeepLinkGroupId,
    resolveSelectedGroup,
    type SharedGroup,
  } from '../../lib/groups-data';
  import {
    CONJUNCTION_DEFAULT_CRITERIA,
    CONJUNCTION_DEFAULT_PROPAGATOR,
    CONJUNCTION_DEFAULT_RESULT_MODE,
    CONJUNCTION_ONE_OFF_DEFAULT_WINDOW,
    CONJUNCTION_STREAM_TICK_INTERVAL_MS,
    buildLiveCardView,
    buildOneOffMessage,
    buildPropagatorRows,
    buildProvenanceView,
    buildResultRows,
    buildResultsCsvOutput,
    buildResultsJsonOutput,
    buildSourceRowViews,
    buildSourceRows,
    buildTargetPills,
    buildTargetStripView,
    bumpHardBodyRadius,
    bumpMissDistance,
    bumpOneOffWindow,
    bumpScreenWindow,
    bumpStepSize,
    cyclePcThreshold,
    loadConjunctionSources,
    moveSourceOrder,
    nextStreamTick,
    propagatorName,
    selectPropagator,
    toggleSourceOff,
    type ConjunctionCriteria,
    type ConjunctionPropagatorKey,
    type ConjunctionSourcesData,
  } from '../../lib/conjunction-data';
  import type { QueryOutputMode } from '../../lib/query-data';
  import type { SdnApiClient } from '../../../lib/auth/sdn-api-client';

  // `show3dLink` gates the "OPEN IN 3D" (→ `/orbital?group=`) affordance. It
  // defaults to `true` so the full SpaceAware console (`ConsoleShell`) is
  // unchanged; the standalone conjunction ship (`ConjunctionApp`) passes `false`
  // because `/orbital` is descoped and not bundled there (C3 disposition).
  let {
    apiClient,
    navigate,
    show3dLink = true,
  }: { apiClient: SdnApiClient; navigate: (path: string) => void; show3dLink?: boolean } = $props();

  // SCREEN TARGET (real groups)
  let groups = $state<SharedGroup[]>([]);
  let selectedGroupId = $state<string | null>(null);

  // ① DATA SOURCES (real surfaces + client-side precedence/toggle)
  let sourcesData = $state<ConjunctionSourcesData | null>(null);
  let sourceOrder = $state<string[]>([]);
  let sourceOff = $state<Record<string, boolean>>({});

  // ② PROPAGATOR / ③ CRITERIA (honest, unused client-side UI state)
  let propagator = $state<ConjunctionPropagatorKey>(CONJUNCTION_DEFAULT_PROPAGATOR);
  let criteria = $state<ConjunctionCriteria>({ ...CONJUNCTION_DEFAULT_CRITERIA });

  // SCREENING RESULTS mode
  let resultMode = $state<QueryOutputMode>(CONJUNCTION_DEFAULT_RESULT_MODE);

  // LIVE STREAM STATUS (demo ticker) + ONE-OFF RUN popover (demo)
  let live = $state(true);
  let streamTick = $state(0);
  let oneOffOpen = $state(false);
  let oneOffWindow = $state(CONJUNCTION_ONE_OFF_DEFAULT_WINDOW);
  let oneOffRan = $state(false);

  const selectedGroup = $derived(groups.length > 0 ? resolveSelectedGroup(groups, selectedGroupId) : null);
  const resolvedGroupId = $derived(selectedGroup?.id ?? null);
  const targetPills = $derived(buildTargetPills(groups, resolvedGroupId));
  const targetStrip = $derived(selectedGroup ? buildTargetStripView(selectedGroup) : null);

  const baseSourceRows = $derived(buildSourceRows(sourcesData?.peers ?? [], sourcesData?.channels ?? [], sourcesData?.stats ?? null));
  const sourceRowViews = $derived(buildSourceRowViews(baseSourceRows, sourceOrder, sourceOff));
  const enabledSourceCount = $derived(sourceRowViews.filter((r) => !r.off).length);

  const propagatorRows = $derived(buildPropagatorRows(propagator));
  const selectedPropagatorLabel = $derived(propagatorName(propagator));

  const resultRows = $derived(buildResultRows(criteria));
  const resultJson = $derived(buildResultsJsonOutput(criteria));
  const resultCsv = $derived(buildResultsCsvOutput(criteria));

  const liveCard = $derived(buildLiveCardView(live, streamTick, enabledSourceCount, selectedPropagatorLabel));
  const oneOffMessage = $derived(buildOneOffMessage(oneOffRan, oneOffWindow));

  const provenanceView = buildProvenanceView();

  onMount(() => {
    const loadedGroups = loadSharedGroups(window.localStorage);
    groups = loadedGroups;
    try {
      const { group: groupParam } = parseConsoleDeepLinkQuery(window.location.search);
      const resolved = resolveDeepLinkGroupId(loadedGroups, groupParam);
      if (resolved) selectedGroupId = resolved;
    } catch {
      // Malformed query string — nothing to select.
    }

    void loadConjunctionSources(apiClient).then((data) => {
      sourcesData = data;
      sourceOrder = buildSourceRows(data.peers, data.channels, data.stats).map((r) => r.id);
    });

    const interval = setInterval(() => {
      if (live) streamTick = nextStreamTick(streamTick);
    }, CONJUNCTION_STREAM_TICK_INTERVAL_MS);
    return () => clearInterval(interval);
  });

  function selectGroupPill(id: string) {
    selectedGroupId = id;
  }

  function open3d() {
    if (!targetStrip) return;
    navigate(targetStrip.openIn3dPath);
  }

  function moveSource(id: string, direction: -1 | 1) {
    sourceOrder = moveSourceOrder(sourceOrder, id, direction);
  }

  function toggleSource(id: string) {
    sourceOff = toggleSourceOff(sourceOff, id);
  }

  function selectPropagatorHandler(key: ConjunctionPropagatorKey) {
    propagator = selectPropagator(propagator, key);
  }

  function setResultMode(mode: QueryOutputMode) {
    resultMode = mode;
  }

  function toggleLive() {
    live = !live;
  }

  function toggleOneOff() {
    oneOffOpen = !oneOffOpen;
  }

  function runOneOff() {
    oneOffRan = true;
    oneOffOpen = false;
  }
</script>

<div class="sdn-conj-root">
  <ConjunctionTaskPanel
    {targetPills}
    selectedGroupId={resolvedGroupId}
    onSelectGroup={selectGroupPill}
    {targetStrip}
    {show3dLink}
    onOpen3d={open3d}
    sourceRows={sourceRowViews}
    onMoveSource={moveSource}
    onToggleSource={toggleSource}
    {propagatorRows}
    onSelectPropagator={selectPropagatorHandler}
    {criteria}
    onMissDown={() => (criteria = bumpMissDistance(criteria, -0.5))}
    onMissUp={() => (criteria = bumpMissDistance(criteria, 0.5))}
    onWindowDown={() => (criteria = bumpScreenWindow(criteria, -12))}
    onWindowUp={() => (criteria = bumpScreenWindow(criteria, 12))}
    onHbrDown={() => (criteria = bumpHardBodyRadius(criteria, -5))}
    onHbrUp={() => (criteria = bumpHardBodyRadius(criteria, 5))}
    onStepDown={() => (criteria = bumpStepSize(criteria, -30))}
    onStepUp={() => (criteria = bumpStepSize(criteria, 30))}
    onCyclePc={() => (criteria = cyclePcThreshold(criteria))}
    {liveCard}
    onToggleLive={toggleLive}
    {oneOffOpen}
    {oneOffWindow}
    {oneOffMessage}
    onToggleOneOff={toggleOneOff}
    onOneOffDown={() => (oneOffWindow = bumpOneOffWindow(oneOffWindow, -1))}
    onOneOffUp={() => (oneOffWindow = bumpOneOffWindow(oneOffWindow, 1))}
    onRunOneOff={runOneOff}
  />

  <ConjunctionResultsPanel {resultMode} onSetMode={setResultMode} {resultRows} {resultJson} {resultCsv} />

  <ConjunctionProvenancePanel {provenanceView} />
</div>

<style>
  .sdn-conj-root {
    display: grid;
    grid-template-columns: repeat(12, minmax(0, 1fr));
    gap: 14px;
    align-content: start;
  }
</style>

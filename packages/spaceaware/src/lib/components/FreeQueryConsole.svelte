<svelte:options runes={true} />

<script>
  import {
    patchQueryParams,
    queryBusy,
    queryParams,
    queryResult,
    queryStatus,
    queryVisibleFields,
    setQueryStatus,
  } from "../stores/queryStore.js";
  import {
    buildRequestUrl,
    normalizeApiBase,
    runDataQuery,
    validateQueryParams,
  } from "../api/dataApi.js";
  import { buildShareUrl } from "../share/shareLinks.js";

  let lastRequestUrl = $state("");
  let visibleFields = $derived($queryVisibleFields);

  function isVisible(field) {
    return visibleFields.has(field);
  }

  function updateParam(key, value) {
    patchQueryParams({ [key]: value });
  }

  async function copyText(value, successMessage) {
    try {
      await navigator.clipboard.writeText(value);
      setQueryStatus(successMessage, "good");
    } catch (error) {
      setQueryStatus(`Clipboard copy failed: ${String(error)}`, "bad");
    }
  }

  function currentParams() {
    return {
      ...$queryParams,
      apiBase: normalizeApiBase($queryParams.apiBase),
    };
  }

  async function handleRunQuery() {
    const params = currentParams();
    const validationError = validateQueryParams(params);
    if (validationError) {
      setQueryStatus(validationError, "bad");
      return;
    }

    queryBusy.set(true);
    queryResult.set("Loading...");
    setQueryStatus("Running query...", "warn");

    try {
      const result = await runDataQuery(params);
      lastRequestUrl = result.requestUrl;

      if (!result.ok) {
        setQueryStatus(`Request failed (${result.status})`, "bad");
        queryResult.set(result.payload);
        return;
      }

      setQueryStatus(`OK: ${result.contentType || "response"} (${result.status})`, "good");
      queryResult.set(result.payload);
    } catch (error) {
      setQueryStatus(`Request error: ${String(error)}`, "bad");
      queryResult.set(String(error));
    } finally {
      queryBusy.set(false);
    }
  }

  async function handleCopyRequest() {
    const params = currentParams();
    const validationError = validateQueryParams(params);
    if (validationError) {
      setQueryStatus(validationError, "bad");
      return;
    }
    await copyText(buildRequestUrl(params), "Request URL copied.");
  }

  async function handleCopyShare() {
    const params = currentParams();
    const validationError = validateQueryParams(params);
    if (validationError) {
      setQueryStatus(validationError, "bad");
      return;
    }

    const shareUrl = buildShareUrl(params, window.location.href);
    await copyText(shareUrl, "Beta share link copied.");
  }
</script>

<section class="card">
  <h2>Free Layer Console</h2>

  <div class="row">
    <label for="apiBase">API Base URL</label>
    <input
      id="apiBase"
      placeholder="https://spaceaware.io"
      value={$queryParams.apiBase}
      oninput={(event) => updateParam("apiBase", event.currentTarget.value)}
    />
  </div>

  <div class="row">
    <label for="endpoint">Endpoint</label>
    <select
      id="endpoint"
      value={$queryParams.endpoint}
      onchange={(event) => updateParam("endpoint", event.currentTarget.value)}
    >
      <option value="health">Health</option>
      <option value="omm">OMM Lookup</option>
      <option value="mpe">MPE Emit (from OMM)</option>
      <option value="cat">CAT Lookup</option>
    </select>
  </div>

  {#if isVisible("day")}
    <div class="row">
      <label for="day">Day (UTC)</label>
      <input
        id="day"
        type="date"
        value={$queryParams.day}
        oninput={(event) => updateParam("day", event.currentTarget.value)}
      />
    </div>
  {/if}

  {#if isVisible("norad")}
    <div class="row">
      <label for="noradCatId">NORAD CAT ID</label>
      <input
        id="noradCatId"
        type="number"
        min="1"
        placeholder="25544"
        value={$queryParams.noradCatId}
        oninput={(event) => updateParam("noradCatId", event.currentTarget.value)}
      />
    </div>
  {/if}

  {#if isVisible("entity")}
    <div class="row">
      <label for="entityId">Entity ID</label>
      <input
        id="entityId"
        placeholder="1998-067A"
        value={$queryParams.entityId}
        oninput={(event) => updateParam("entityId", event.currentTarget.value)}
      />
    </div>
  {/if}

  {#if isVisible("limit")}
    <div class="row">
      <label for="limit">Limit</label>
      <input
        id="limit"
        type="number"
        min="1"
        max="1000"
        value={$queryParams.limit}
        oninput={(event) => updateParam("limit", event.currentTarget.value)}
      />
    </div>
  {/if}

  {#if isVisible("format")}
    <div class="row">
      <label for="format">Response</label>
      <select
        id="format"
        value={$queryParams.format}
        onchange={(event) => updateParam("format", event.currentTarget.value)}
      >
        <option value="flatbuffers">FlatBuffers</option>
        <option value="json">JSON</option>
      </select>
    </div>
  {/if}

  <div class="actions">
    <button onclick={handleRunQuery} disabled={$queryBusy}>Run Query</button>
    <button class="alt" onclick={handleCopyShare}>Copy Beta Share Link</button>
    <button class="ghost" onclick={handleCopyRequest}>Copy Request URL</button>
  </div>

  <div class={`status ${$queryStatus.kind || ""}`}>{$queryStatus.message}</div>
  <pre>{$queryResult}</pre>

  {#if lastRequestUrl}
    <p class="small">
      Last request:
      <a href={lastRequestUrl} target="_blank" rel="noreferrer">{lastRequestUrl}</a>
    </p>
  {/if}
</section>

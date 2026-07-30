<script>
  /**
   * THIS NODE identity editor (graph task nst-node-edit-permissions-ui
   * deliverable 2; wire contract nst-node-admin-contract §6).
   *
   * Covers every epm.Profile field. Values start from the node's CURRENT
   * profile (GET /api/node/epm/json) and from nothing else — no invented
   * contact data, no placeholder prefill, empty stays empty. Save PUTs the
   * complete profile to /api/node/epm (Admin session + CSRF header) and
   * hands the caller the refreshed JSON + vCard so THIS NODE re-renders
   * without waiting for the next status frame.
   *
   * Styled only with theme.js tokens + design components.
   */
  import { onMount } from 'svelte';
  import GBtn from 'spaceaware-student-sdn/src/lib/components/GBtn.svelte';
  import StatusChip from 'spaceaware-student-sdn/src/lib/components/StatusChip.svelte';
  import { theme } from 'spaceaware-student-sdn/src/lib/theme.js';
  import { apiFetch, apiText, apiPutExpectBinary, describeApiError } from './api.js';
  import {
    PROFILE_FIELDS,
    ADDRESS_FIELDS,
    emptyProfileForm,
    profileFormFromJson,
    profileFormToBody,
    profileFormDirty,
  } from './profile.js';

  /** @type {{ onCancel: () => void, onSaved: (r: {json: any, vcard: string}) => void }} */
  let { onCancel, onSaved } = $props();

  let form = $state(emptyProfileForm());
  let baseline = $state(emptyProfileForm());
  let loading = $state(true);
  let saving = $state(false);
  let error = $state('');

  const dirty = $derived(profileFormDirty(form, baseline));

  onMount(async () => {
    try {
      const json = await apiFetch('/api/node/epm/json');
      form = profileFormFromJson(json);
      baseline = profileFormFromJson(json);
    } catch (err) {
      // A node with no EPM yet answers 404 — that is an EMPTY profile, not
      // an error to prefill around.
      if (err?.status !== 404) error = describeApiError(err);
    } finally {
      loading = false;
    }
  });

  async function save(e) {
    e?.preventDefault?.();
    if (saving) return;
    error = '';
    saving = true;
    try {
      await apiPutExpectBinary('/api/node/epm', profileFormToBody(form));
      const [json, vcard] = await Promise.all([
        apiFetch('/api/node/epm/json').catch(() => null),
        apiText('/api/node/epm/vcard'),
      ]);
      if (json) {
        form = profileFormFromJson(json);
        baseline = profileFormFromJson(json);
      }
      onSaved({ json, vcard });
    } catch (err) {
      error = describeApiError(err);
    } finally {
      saving = false;
    }
  }
</script>

<form class="edit" onsubmit={save}>
  {#if loading}
    <div class="loading" style="color:{theme.textDim};">Reading the node's current profile…</div>
  {:else}
    <div class="grid">
      {#each PROFILE_FIELDS as f (f.key)}
        <label class="field">
          <span class="k" style="color:{theme.textMuted};">{f.label}</span>
          <input
            type={f.type ?? 'text'}
            bind:value={form[f.key]}
            disabled={saving}
            style="color:{theme.textBright};border-color:{theme.hairline};background:{theme.inputWell};"
          />
        </label>
      {/each}
    </div>

    <div class="section" style="border-color:{theme.divider};">
      <div class="k head" style="color:{theme.textMuted};">ADDRESS</div>
      <div class="grid">
        {#each ADDRESS_FIELDS as f (f.key)}
          <label class="field">
            <span class="k" style="color:{theme.textMuted};">{f.label}</span>
            <input
              type="text"
              bind:value={form.address[f.key]}
              disabled={saving}
              style="color:{theme.textBright};border-color:{theme.hairline};background:{theme.inputWell};"
            />
          </label>
        {/each}
      </div>
    </div>

    <div class="section" style="border-color:{theme.divider};">
      <div class="k head" style="color:{theme.textMuted};">ALTERNATE NAMES</div>
      <textarea
        bind:value={form.alternate_names}
        rows="3"
        disabled={saving}
        spellcheck="false"
        style="color:{theme.textBright};border-color:{theme.hairline};background:{theme.inputWell};"
      ></textarea>
      <div class="hint" style="color:{theme.textFaint};">One name per line. Blank lines are ignored.</div>
    </div>

    {#if form.photo_data_url}
      <div class="section" style="border-color:{theme.divider};">
        <div class="photo">
          <div>
            <div class="k head" style="color:{theme.textMuted};">PHOTO</div>
            <div class="hint" style="color:{theme.textFaint};">
              Carried through unchanged on save (the node replaces the whole profile,
              so it has to travel with every edit).
            </div>
          </div>
          <div class="photo-actions">
            <StatusChip label="PRESENT" color={theme.ice} dot={false} />
            <GBtn
              title="Remove the published photo"
              variant="destructive"
              disabled={saving}
              onclick={(e) => { e.preventDefault(); form.photo_data_url = ''; }}
            >REMOVE</GBtn>
          </div>
        </div>
      </div>
    {/if}

    {#if error}
      <div class="err" style="color:{theme.red};border-color:{theme.red};">{error}</div>
    {/if}

    <div class="foot" style="border-color:{theme.divider};">
      <span class="state" style="color:{theme.textFaint};">
        {dirty ? 'UNSAVED CHANGES' : 'NO CHANGES'}
      </span>
      <span class="btns">
        <GBtn title="Discard changes" disabled={saving} onclick={(e) => { e.preventDefault(); onCancel(); }}>CANCEL</GBtn>
        <GBtn title="Save and republish this node's identity" variant="primary" disabled={saving || !dirty}>
          {saving ? 'SAVING…' : 'SAVE & REPUBLISH'}
        </GBtn>
      </span>
    </div>
  {/if}
</form>

<style>
  .edit { display: flex; flex-direction: column; gap: 12px; font-family: 'IBM Plex Mono', ui-monospace, monospace; }
  .loading { font-size: var(--sdn-fs-value); line-height: var(--sdn-lh-value); letter-spacing: 0.06em; padding: 10px 0; }
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(230px, 1fr));
    gap: 11px 16px;
  }
  .field { display: flex; flex-direction: column; gap: 5px; min-width: 0; }
  .k { font-size: var(--sdn-fs-body); line-height: var(--sdn-lh-body); letter-spacing: 0.16em; }
  .k.head { display: block; margin-bottom: 8px; }
  input,
  textarea {
    border: 1px solid;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: var(--sdn-fs-value); line-height: var(--sdn-lh-value);
    letter-spacing: 0.03em;
    padding: 7px 9px;
    outline: none;
    min-width: 0;
    resize: vertical;
  }
  textarea { width: 100%; }
  .section { border-top: 1px solid; padding-top: 12px; }
  .hint { font-size: var(--sdn-fs-body); letter-spacing: 0.04em; line-height: var(--sdn-lh-body); margin-top: 6px; }
  .photo { display: flex; align-items: flex-start; justify-content: space-between; gap: 14px; flex-wrap: wrap; }
  .photo-actions { display: flex; align-items: center; gap: 8px; }
  .err {
    border: 1px solid;
    padding: 9px 11px;
    font-size: var(--sdn-fs-note);
    letter-spacing: 0.04em;
    line-height: var(--sdn-lh-note);
  }
  .foot {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    flex-wrap: wrap;
    border-top: 1px solid;
    padding-top: 13px;
  }
  .state { font-size: var(--sdn-fs-label); line-height: var(--sdn-lh-label); letter-spacing: 0.16em; }
  .btns { display: inline-flex; gap: 9px; }
</style>

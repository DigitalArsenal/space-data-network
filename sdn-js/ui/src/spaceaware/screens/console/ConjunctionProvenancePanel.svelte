<script lang="ts">
  /**
   * "PROVENANCE · ENCRYPTED WORKFLOW" panel (loop task U3.9). Ground truth:
   * `SDN Console.dc.html:723-740` — MODE/MODULE/RESULT CHANNEL/GRANT field
   * rows, QUERY HASH + RESULT HASH mono blocks, and a boxed assessor-enclave
   * note. Every field here is static fixture text with no backing daemon
   * surface (see `conjunction-data.ts`'s `buildProvenanceView` doc comment)
   * — DEMO-tagged next to the panel kicker per decision D4.
   */
  import { CONJUNCTION_PROVENANCE_DEMO_TAG_TITLE, type ConjunctionProvenanceView } from '../../lib/conjunction-data';

  let { provenanceView }: { provenanceView: ConjunctionProvenanceView } = $props();
</script>

<section class="sdn-conj-provenance">
  <span class="sdn-conj-provenance-kicker-row">
    <span class="sdn-conj-provenance-kicker">PROVENANCE · ENCRYPTED WORKFLOW</span>
    <span class="sdn-conj-demo-tag" title={CONJUNCTION_PROVENANCE_DEMO_TAG_TITLE}>DEMO</span>
  </span>

  <div class="sdn-conj-provenance-fields">
    <div class="sdn-conj-provenance-field">
      <span class="sdn-conj-provenance-field-label">MODE</span>
      <span class="sdn-conj-provenance-field-value">{provenanceView.mode}</span>
    </div>
    <div class="sdn-conj-provenance-field">
      <span class="sdn-conj-provenance-field-label">MODULE</span>
      <span class="sdn-conj-provenance-field-value">{provenanceView.module}</span>
    </div>
    <div class="sdn-conj-provenance-field">
      <span class="sdn-conj-provenance-field-label">RESULT CHANNEL</span>
      <span class="sdn-conj-provenance-field-value sdn-conj-provenance-field-value--channel">{provenanceView.resultChannel}</span>
    </div>
    <div class="sdn-conj-provenance-field">
      <span class="sdn-conj-provenance-field-label">GRANT</span>
      <span class="sdn-conj-provenance-field-value sdn-conj-provenance-field-value--grant">{provenanceView.grant}</span>
    </div>
  </div>

  <div class="sdn-conj-provenance-hashes">
    <div>
      <div class="sdn-conj-provenance-hash-label">QUERY HASH</div>
      <div class="sdn-conj-provenance-hash-value">{provenanceView.queryHash}</div>
    </div>
    <div>
      <div class="sdn-conj-provenance-hash-label">RESULT HASH</div>
      <div class="sdn-conj-provenance-hash-value">{provenanceView.resultHash}</div>
    </div>
  </div>

  <div class="sdn-conj-provenance-enclave">
    <span class="sdn-conj-provenance-enclave-glyph">◈</span>
    <span class="sdn-conj-provenance-enclave-text">{provenanceView.enclaveNote}</span>
  </div>
</section>

<style>
  .sdn-conj-provenance {
    grid-column: span 5;
    background: linear-gradient(178deg, #16252f, #0a141b);
    border: 1px solid rgba(90, 150, 180, 0.22);
    box-shadow: inset 0 1px 0 rgba(150, 210, 240, 0.14);
    padding: 15px 16px;
  }

  .sdn-conj-provenance-kicker-row {
    display: flex;
    align-items: center;
    gap: 7px;
    margin-bottom: 12px;
  }

  .sdn-conj-provenance-kicker {
    font-size: 10px;
    letter-spacing: 0.2em;
    color: #5a7a8a;
  }

  .sdn-conj-provenance-fields {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }

  .sdn-conj-provenance-field {
    display: flex;
    justify-content: space-between;
    align-items: baseline;
    gap: 10px;
  }

  .sdn-conj-provenance-field-label {
    font-size: 11.5px;
    color: #7d929b;
    letter-spacing: 0.08em;
    flex: none;
  }

  .sdn-conj-provenance-field-value {
    font-size: 12px;
    color: #cfe3ec;
    overflow-wrap: anywhere;
    text-align: right;
  }

  .sdn-conj-provenance-field-value--channel {
    color: #9fd4f5;
  }

  .sdn-conj-provenance-field-value--grant {
    color: #5ad6a0;
  }

  .sdn-conj-provenance-hashes {
    border-top: 1px solid rgba(90, 150, 180, 0.12);
    margin-top: 14px;
    padding-top: 12px;
    display: flex;
    flex-direction: column;
    gap: 9px;
  }

  .sdn-conj-provenance-hash-label {
    font-size: 9.5px;
    letter-spacing: 0.14em;
    color: #5a7a8a;
  }

  .sdn-conj-provenance-hash-value {
    font-size: 12px;
    color: #cfe3ec;
    margin-top: 2px;
    overflow-wrap: anywhere;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
  }

  .sdn-conj-provenance-enclave {
    display: flex;
    align-items: flex-start;
    gap: 9px;
    border: 1px solid rgba(53, 201, 216, 0.28);
    background: rgba(53, 201, 216, 0.05);
    padding: 10px 11px;
    margin-top: 14px;
  }

  .sdn-conj-provenance-enclave-glyph {
    font-size: 15.5px;
    color: #35c9d8;
    flex: none;
  }

  .sdn-conj-provenance-enclave-text {
    font-size: 11px;
    color: #9fe9f2;
    line-height: 1.5;
  }

  /* -- DEMO tag (same style as GroupsView.svelte's .sdn-groups-demo-tag) -- */

  .sdn-conj-demo-tag {
    padding: 1px 5px;
    border: 1px solid rgba(255, 208, 137, 0.5);
    color: #ffd089;
    font-family: 'IBM Plex Mono', ui-monospace, monospace;
    font-size: 8px;
    letter-spacing: 0.12em;
    flex: none;
  }
</style>

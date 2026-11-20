# Snapshot guard: a short pull does not replace a whole catalog

A lane declared `snapshot_batches` publishes each batch as the complete dataset. The CelesTrak GP lane is one: every batch is the full catalog, and the console loads one batch. If the upstream answer is cut short, the short batch replaces everything.

That happened on 2026-11-20. Every GP run of the two days before published 32,917 to 32,958 records. The 17:32 and 20:35 UTC runs got a CelesTrak answer that ended near 3.7 MB, at NORAD 63,459 and 63,416. They published 24,973 and 24,929 records, and about 8,000 objects disappeared from the catalog the console loads.

## Configuration

```yaml
publishing:
  retention:
    lanes:
      - schema: OMM.fbs
        source_name: celestrak-gp
        keep_series: 2
        snapshot_batches: true
        min_record_ratio: 0.95
      - schema: MPE.fbs
        source_name: celestrak-gp
        keep_series: 2
        snapshot_batches: true
        min_record_ratio: 0.95
```

`min_record_ratio` applies only with `snapshot_batches`. With it set, a batch holding fewer records than ratio x the newest published series of the lane (from another batch) is refused:

- Nothing is exported, pinned or announced, and retention retires nothing. The lane keeps advertising the series it had.
- The batch's records stay in the store. Its publication entry is closed, so the refusal is not retried.
- Refused batches record no series, so the next batch is measured against the last batch that was published.
- The node logs one line, and the publication fails with the same text at step `snapshot guard`:

```
snapshot guard: snapshot batch "<batch>" of OMM.fbs space-data-network-02/celestrak-gp holds 24929 records, under 31298 (0.95 x 32945, the series published <time> from batch "<batch>"): REFUSED, the lane keeps advertising that series; if the source really shrank, lower or remove min_record_ratio for this lane
```

Unset or 0 turns the guard off (the default).

## Why 0.95

From one run to the next the GP catalog moved by at most 37 records (0.11 percent) across the 18 runs of 11-18 to 11-20. A large launch adds a few hundred and decays remove a few. The short pulls lost 24 percent. At 0.95 the floor sits about 1,650 records under the last full catalog: far outside the normal movement, and far above a cut answer.

If the source really shrinks by more than 5 percent (a catalog policy change), the lane stops publishing until an operator lowers or removes the ratio. The log line says so.

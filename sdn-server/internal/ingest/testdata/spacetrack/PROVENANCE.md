# Space-Track supplemental-ingest test fixtures

All fixtures are real, trimmed Space-Track API responses captured during the
A2.2c-ST live smoke test on 2026-07-13 (authenticated M2M session, owner-
provisioned credentials). No credentials are embedded. These drive the
fixture-only unit tests; CI makes no live calls.

| file | source request | notes |
|------|----------------|-------|
| `publicfiles-dirs.json` | `GET publicfiles/query/class/dirs` | verbatim. Array of directory slug strings `public-data-files-<ID>-<slug>-prod`. NASA-JSC ids 23643/23644/23645, Kuiper 24206, SpaceX 552. |
| `publicfiles-loadpublicdata.json` | `GET publicfiles/query/class/loadpublicdata` | verbatim FLAT listing of every file the account can access (3 NASA-JSC files). `?id=<dir>` is IGNORED by the endpoint — it is not per-directory; new provider files simply appear here when shared. |
| `publicfiles-iss15day.zip` | `GET publicfiles/query/class/download?name=NASAJSC_Ephemeris_23644_15Day_...zip` | nested-zip structure mirrors the real download (outer zip → `15Day_SpaceTrack_Public_071326.zip` → OEM xml + TrajectorySummary.txt). The inner OEM is `oem-iss-trimmed.xml` (5 of 5400+ state vectors). |
| `oem-iss-trimmed.xml` | inner OEM of the download above | CCSDS OEM XML (NDM v1.0), REF_FRAME=EME2000, TIME_SYSTEM=UTC, DOY epochs. First 5 state vectors kept; trim documented in an inline XML comment. Original OBJECT_NAME=8000 / OBJECT_ID=01 are NASA-JSC internal designators, preserved as-declared. |
| `gp-current-sample.json` | `GET basicspacedata/query/class/gp/DECAY_DATE/null-val/orderby/NORAD_CAT_ID asc/limit/3/format/json` | verbatim first 3 current-`gp` CCSDS OMM JSON records (schema-exact keys, ORIGINATOR "18 SPCS"). |

Rate rules observed during capture: <30 req/min, <300 req/hr, halt on any
non-200. Total live requests for the whole A2.2c-ST task: well under the 15-cap.

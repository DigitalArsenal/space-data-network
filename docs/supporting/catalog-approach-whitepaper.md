# An Attributed, HPOP-First Orbital Catalog for Space Data Network

Technical whitepaper | Version 1.0 | Evidence cutoff: 21 September 2026

Space Data Network / Digital Arsenal

## Executive summary

Space Data Network (SDN) is developing a reproducible, multi-provider orbital catalog for distributed data sharing and conjunction assessment. Its central product is a versioned view of attributed source records: an object identity, a selected orbital solution, its provenance, its uncertainty status, and an explicit policy explaining why that solution was selected. Original provider products remain available as distinct records. A catalog revision is authoritative within its declared policy and evidence, rather than an assertion that one provider is universally correct.

The design is HPOP-first. Where adequate states, observations, or ephemerides exist, numerical high-precision orbit propagation is the primary computational path. SGP4 remains essential for native general-perturbations (GP) products, interoperability, and comparison with SOCRATES. A TLE or SGP4 OMM is not reinterpreted as an osculating numerical-propagation state. A derived SGP4 product is obtained by fitting and validating the SGP4 model against a selected reference arc, with the approximation recorded.

The initial Vimpel implementation converts osculating elements analytically into epoch position and velocity, retains the provider's native identity and source hashes, propagates through the existing HPOP module, and compares against separately supplied provider positions. One constrained initial-state refinement reduced withheld-position RMS discrepancies from 6.04-10.71 km to 52.82-69.93 m on three four-hour arcs. All 13,808 rows in the acquired element-table snapshot converted successfully. These results establish bounded consistency with provider products, not independent absolute accuracy, catalog-wide accuracy, or certified collision probabilities. [R1-R3]

This paper includes Space Mapper's Chinese AOE catalog as a proposed additional source. Its own-orbit, international, and mixed products require separate provenance so republished information is not counted twice. AOE has not been included in the numerical results reported here. The previously tested China Space Station provider feed is a different source and is not evidence of ingesting the AOE catalog. [R4-R7]

Implemented capabilities and proposed operational requirements are identified throughout. Automatic matching from Vimpel's datefirst file, accepted identity persistence, full catalog covariance calibration, and continuous development-node conjunction integration remain incomplete in the evidence baseline. The intended UT Austin conjunction service is an operational placement objective; this paper does not establish its current readiness. [R1-R3]

### Reader guide

1. Architecture and meaning of authority
2. Data inventory and evidence boundaries
3. Chinese AOE integration
4. Normalization and epoch-state physics
5. Identity matching and orbit selection
6. HPOP fitting and measured results
7. Uncertainty, SGP4 and conjunction validation
8. Publication, feedback and operator experience
9. Acceptance plan and reproducibility
10. References

## 1. Architecture and the meaning of authority

### 1.1 Three separate decisions

The system must distinguish object association, orbit selection, and estimation. Association asks whether two provider records describe the same physical object. Selection chooses which available orbital solution is suitable for a particular epoch and use. Estimation derives a new solution from observations or reference trajectories under an explicit dynamical model. Success at one stage is not proof of success at the others.

The proposed processing chain is:

`Acquire -> preserve source -> normalize -> propose associations -> test physics -> select or fit -> validate -> publish -> screen conjunctions`

The implemented Catalog Editor composes immutable source editions and preserves selected record bytes. Its matching method evaluates proposed associations without automatically merging identities. The Vimpel reader and epoch fitting methods are implemented. Automated end-to-end orchestration across all these stages is a remaining integration task. [R1-R3]

### 1.2 Formal records and snapshots

Define a source record as R = (provider, nativeID, edition, sourceHash, epoch, timeScale, frame, productType, data, lineage). The tuple (provider, nativeID) identifies a provider's assertion; an edition and hash identify the specific evidence. Native identifiers retain their original spelling alongside any documented normalization. Observation time, solution epoch, issue time, and retrieval time are separate fields.

An association A = (R_a, R_b, verdict, policyHash, evidenceRefs) links assertions. A selected solution S = (objectID, stateRef, validityInterval, modelRef, uncertaintyStatus, lineage) references the chosen or derived product. A catalog snapshot C = (parentSnapshot, recipeHash, associationSet, solutionSet, sourceHeads) binds the decision to immutable inputs. These tuples define the design's logical contract; they are not a claim that every proposed field already exists in an SDS schema.

Authority is reproducible: given identical snapshots, module artifacts and policy, a consumer should obtain the same selection and verdicts within declared numerical tolerances. Consumers may adopt different source policies while preserving shared evidence. Content hashes establish byte identity; publisher signatures establish attribution. Neither establishes that an orbit is physically correct.

### 1.3 HPOP-first, with native-model discipline

HPOP is a numerical propagation capability, not an accuracy label. Its validity depends on the initial state, force model, frame and time conversions, environmental inputs, maneuver treatment and uncertainty model. A fresh but erroneous record must not displace a better-supported solution solely because it is newer.

The intended base catalog maintains the latest usable solution from each provider, plus history, and evaluates candidate solutions at a common assessment epoch. It does not average incompatible epochs or models. When adequate HPOP inputs are unavailable, retain a visibly qualified native-model solution or an explicit gap. Do not create an apparently complete catalog by concealing model substitutions.

## 2. Data inventory and evidence boundaries

The datasets below have different evidentiary roles. Historical acquisition and transport tests do not establish physical orbit accuracy or current service uptime. Raw restricted provider files are not reproduced in this paper.

| Dataset or family | Role and evidence at cutoff |
| --- | --- |
| Vimpel osculating element table | Acquired snapshot: 13,808 rows; all converted to attributed epoch OPM states. Source SHA-256 in section 9. |
| Vimpel rectangular ephemeris archive | Acquired September 2026 archive. Audit found 6,654 members matching the element table by native identity and exact epoch. A 64-object diagnostic and three-object HPOP refinement study were performed. |
| Vimpel datefirst and alias products | Acquired supporting identity evidence. datefirst proposes native-to-NORAD associations; automatic candidate generation and accepted-binding persistence remain incomplete. |
| CelesTrak GP / supplemental GP and SOCRATES | Required interoperability and conjunction comparison sources. Acquire catalog/report snapshots through the celestrak.eth test node, not this local development machine. No completed SOCRATES agreement result is asserted here. |
| Space Mapper / AOE, China | Public format and API documentation reviewed for this paper. Proposed integration; no acquired catalog snapshot or numerical validation is claimed. |
| Owner/operator and precise products | Historical provider-fleet evidence described below. Potentially valuable reference or primary solutions, subject to per-product normalization and validation. |
| Analytical cases and runtime fixtures | Used for converter, matcher and fitting tests. Software correctness evidence, not an independent physical orbit catalog. |

The September provider-fleet report records 13 public-source lanes passing signed publication, independent IPFS retrieval, pagination and byte-hash checks: SpaceX Starlink, Eutelsat OneWeb, Planet Labs, NASA ISS, SES, Intelsat, Telesat, China Space Station, IGS/BKG GPS Precise, ESA GLONASS Precise, ESA Precise Orbit Determination, EUMETSAT, and ESA CPF Predictions. Acquisition was not uniformly complete: the report recorded 38 of 41 SES resources and continuing Starlink acquisition. These development instances were named for sources; they were not services operated by those organizations. [R7]

The same report lists authenticated Spire Global, Space-Track, EDC CPF and Vimpel adapters. Its then-unverified Vimpel acquisition is superseded by the later Vimpel snapshot evidence in this paper; it must not be generalized to the other authenticated sources. [R2, R3, R7]

Each source edition should include format version, product interval, access restrictions, retrieval result and upstream lineage. Repeated downloads or mirrored distribution channels do not constitute independent observations. CPF, for example, is a prediction exchange format; possession of a CPF does not turn its positions into raw tracking observations. [R8]

## 3. Integrating the Chinese AOE catalog

### 3.1 Source identification

For this edition, “Chinese catalog” is interpreted as Space Mapper's AOE catalog; confirmation of the intended provider remains open. Space Mapper advertises three orbital families: self-determined AOE orbits, international multi-source orbits, and a combined standard product. The self-determined family is described as using its parent company's observation network. Treat that as a provider claim to preserve and test, not independent certification of every record. [R4, R5]

The orbit-list API documents bearer-token authentication, a KY channel for AOE and an INTERNATIONAL channel, and TLE, two-line TLE, JSON and OMM-XML responses. Its OMM example declares Earth, TEME, UTC and SGP4. Its JSON example uses a timezone offset. A separate catalog API exposes MIXED, AOE and INTL sources and distinguishes catalog IDs from NORAD IDs. These are documented examples, not proof that every delivered product conforms. [R6, R9]

### 3.2 Proposed adapter contract

Maintain separate provider/channel namespaces, such as spacemapper:KY and spacemapper:INTERNATIONAL. Preserve the requested channel, actual response metadata, native catalog ID, asserted NORAD/designator links, epoch string, format and exact source bytes. Convert timezone-qualified epochs to a declared internal time scale while retaining the original representation. Do not join on a numeric ID without its namespace.

Validate actual responses against their declared model. For an SGP4 mean-element product, evaluate through a verified SGP4 implementation before any numerical-model fit. Do not pass its semimajor axis and mean anomaly through the Vimpel osculating converter. Missing metadata is a validation failure or an explicit incomplete product, not permission to assume J2000 or UTC.

AOE, international and mixed records may share upstream information. The proposed catalog should retain a source-lineage graph and suppress duplicate evidence contributions. Until lineage is established, treat independence as unknown. Conflicting native and international identifiers should produce a review item with retained alternatives.

### 3.3 Admission study

Before admitting AOE solutions, acquire an authorized immutable snapshot, record counts and hashes, confirm pagination/completeness, and test response types against the published documentation. Verify epoch offsets, identifier namespaces, SGP4 behavior and representative orbit regimes. Compare against time-aligned Vimpel, GP and owner/operator products, while tracking shared upstream lineage. Report matches, disagreements and unavailable cases separately.

The required result is a measured coverage and consistency report, not merely a successful HTTP request. No AOE coverage count, improved conjunction accuracy, licensing conclusion, or completed integration is asserted in this whitepaper. The China Space Station feed in section 2 remains a distinct operator-product lane.

## 4. Normalization and epoch-state physics

### 4.1 Vimpel's single-row osculating product

Vimpel's public bulletin describes a 15-column orbital product, with native object number, first observation date, UTC reference epoch, update age, semimajor axis a, inclination i, right ascension of ascending node Omega, eccentricity e, argument of latitude u, argument of perigee omega, effective area/mass, magnitude, and two uncertainty indicators. The dynamical angles are degrees and a is in kilometres. The frame is J2000. This is a provider-specific osculating format, not a generic SGP4 “single-line element” standard. The primary format bulletin is the operative reference; this paper does not claim a separate standardized single-line-element whitepaper. [R3, R10]

At the stated epoch, derive true anomaly nu = u - omega, with angles reduced consistently. For Earth's gravitational parameter mu and semilatus rectum p:

```
p = a (1 - e^2)
r_pf = p / (1 + e cos(nu)) [cos(nu), sin(nu), 0]
v_pf = sqrt(mu / p) [-sin(nu), e + cos(nu), 0]
Q = Rz(Omega) Rx(i) Rz(omega)
r_J2000 = Q r_pf;   v_J2000 = Q v_pf
```

This operation converts an instantaneous state; it does not propagate to another epoch. The implemented reader reuses foundation/orbits, with explicit SI conversion internally and canonical OPM output in km and km/s. It uses mu = 398600.4418 km^3/s^2. The native name remains vimpel:<nativeID>; no NORAD or international designator is fabricated. The source descriptor is retained unchanged. [R1-R3]

### 4.2 Why finite differencing is diagnostic

For position samples spaced by h, a five-point forward derivative at the first sample is:

```
v0 ~= (-25 r0 + 48 r1 - 36 r2 + 16 r3 - 3 r4) / (12 h)
```

The actual provider position spacing is 600 seconds. At high eccentricity, endpoint truncation error can be substantial; rounding is also amplified. The 64-object audit found discrepancies as large as about 1.106 km/s between analytic and endpoint-derived velocities, and up to 1.824 km/s between derivative stencils. Neither comparison establishes which provider product is absolutely accurate. It does rule out unconditional adoption of the endpoint derivative. [R3]

The preferred velocity is the analytic osculating velocity or a validated fitted state. Position-only products remain position-only evidence; the implementation does not insert zero velocities into an OEM. A smaller numerical differentiation step is not automatically better when timestamps and propagated positions have finite precision.

### 4.3 Frame and time discipline

Every transformation must identify its conventions and supporting data. The verification path explicitly converts J2000 to GCRF for HPOP and back for comparison, and UTC sample times to TDB for propagation. Do not silently equate TEME, terrestrial coordinates, J2000 and GCRF. Production transformations requiring Earth orientation or leap-second data must pin those inputs as well as software versions. [R1, R11]

## 5. Rack, stack and match: identity and selection

### 5.1 Candidate generation

The proposed association stage combines provider crosswalks, international designators, native-ID histories and coarse orbital compatibility. Vimpel's datefirst file has the fields Nvym, t_det_v, Nnor and t_det_n. Its association is evidence from a named provider edition; it is not conclusive identity. Preserve dates and leading-zero normalization, and record contradictory or duplicate declarations. [R3]

A name alone is not a join key. Similar trajectories may represent formation members, recent deployments or fragments. Pairwise compatibility is not automatically transitive: A matching B and B matching C does not justify merging all three. Proposed identity groups require component-wide conflict checks and a consistent one-to-one assignment where the catalog semantics require it. Unresolved identities remain separate.

### 5.2 Common-grid physical test

Normalize each candidate trajectory to the same origin, frame, time scale, start epoch and sampling grid, over an overlapping validity interval. The implemented matcher expects already normalized OEM inputs; the caller performs propagation and transformation. It checks position, velocity and derivative consistency against explicit thresholds. Its verdict is a trajectory compatibility assessment, not automatic identity publication. [R2]

For two candidates, define position and velocity discrepancies:

```
dr_k = r_a(t_k) - r_b(t_k)
dv_k = v_a(t_k) - v_b(t_k)
RMS_r = sqrt(sum_k ||dr_k||^2 / N)
MAX_r = max_k ||dr_k||
```

Report RMS, maxima, tested duration, sample count, source epochs and propagation age. Require sufficient coverage; do not accept a match from a single coincident position. Handle maneuvers, discontinuities and non-overlapping arcs explicitly. Thresholds are recipe parameters whose calibration is a separate study, not universal constants.

The intended review outcomes are compatible, rejected, ambiguous or insufficient. Compatibility can support an identity decision but cannot replace contradictory identification evidence. Accepted links should preserve the reviewer or decision policy and evidence references so later observations can reverse a mistaken association without rewriting history.

### 5.3 Selecting the authoritative solution

For each accepted identity, the proposed selection policy first excludes invalid or inapplicable products, then considers validity coverage, maneuvers, uncertainty quality, held-out residuals, propagation age, source lineage and declared source preference. Selection produces a source reference and a reason. It must not splice position from one solution with velocity or covariance from another.

Composition and matching remain separate. The current recipe-v2 editor uses international designators for authoritative composition. Numeric catalog IDs alone must not silently merge objects. Objects without accepted designators remain review candidates even where the current CAT export excludes them. [R2]

## 6. HPOP refinement and the measured Vimpel study

### 6.1 Constrained estimation

Let x0 be the six-component initial state, f_k(x0) the propagated position, and y_k a provider reference position. The implemented refinement forms finite-difference sensitivity columns using one nominal trajectory and six positively perturbed trajectories. It checks their initial-state bindings and requires six numerically independent sensitivity columns. This differentiation estimates sensitivity to the initial state; it does not estimate the initial velocity by differentiating coarse provider positions. [R1]

The linearized update solves the regularized least-squares problem:

```
min_dx sum_(k in training) || W_k^(1/2) (y_k-f_k(x0)-H_k dx) ||^2
       + || L dx ||^2
```

Weights and regularization are declared tuning parameters. A correction outside configured position/velocity scales is rejected. The candidate has stale Keplerian fields cleared and is always returned as candidate-needs-propagation. Repropagation and validation are mandatory; retaining an unvalidated last iterate is not acceptance. An optional prior held-out RMS gate rejects a worsening candidate. The fitting module does not emit a provider covariance or promote a catalog record automatically. [R1]

### 6.2 Experiment and results

Three four-hour arcs contained 25 positions at 600-second spacing. Withholding indices 3, 7, 11, 15, 19 and 23 left 19 training and six withheld positions per case. The tested force model used central Earth gravity, J2-J4 and analytical Sun/Moon perturbations, with RKF78 integration; drag and solar-radiation pressure were disabled. One update was performed per case. [R1]

| Eccentricity | Initial withheld RMS (km) | Fitted withheld RMS (m) | Maximum withheld residual (m) |
| --- | ---: | ---: | ---: |
| 0.004351 | 10.70686 | 69.929 | 97.349 |
| 0.892913 | 6.04357 | 59.097 | 72.843 |
| 0.840641 | 10.41481 | 52.821 | 89.683 |

Both training and withheld maximum residuals passed the example 1 km position gate. That gate is experimental tuning, not a collision-screening or operational safety threshold. Withheld positions were excluded from estimation, but came from the same provider ephemeris product; they are not independent tracking truth. The cases are demonstrations rather than a statistically representative sample. [R1]

Vimpel's published model includes degree-8 Earth gravity, DE405 Sun/Moon, atmosphere and radiation pressure. The tested HPOP configuration does not reproduce it, and the current GOST selector is a placeholder. An effective area/mass value cannot establish separate drag and radiation-pressure coefficients. The fit may absorb model mismatch over four hours; these results do not validate the remainder of a multi-day arc or long-term prediction. [R1, R3, R10]

## 7. Uncertainty, SGP4 and conjunction validation

### 7.1 Covariance must be earned

The long-term catalog should carry defensible uncertainty information where available. Vimpel's two 50%-confidence uncertainty indicators do not define a full six-dimensional covariance, correlations, or a Gaussian distribution. Preserve these scalars with their semantics. Do not turn a fitting regularizer, missing uncertainty, or small residual into a published covariance. [R3, R10]

A proposed observation-based estimator should document measurement errors, model errors and biases; estimate and propagate covariance with the appropriate dynamics; and test calibration on genuinely independent data. In a linearized model, P(t) = Phi P0 Phi^T + Q, with Phi the state-transition matrix and Q a modeled process-noise contribution. Neither Q nor a measurement error distribution has been established by the three-case study. If source correlations are unknown, simple inverse-covariance averaging can overstate confidence. Prefer explicit selection or a separately validated conservative fusion policy.

### 7.2 SGP4 companion products

Native SGP4 products retain their original theory and metadata. A proposed HPOP-to-SGP4 companion path samples the accepted numerical solution over a declared fitting interval, fits SGP4 mean elements, and verifies both training and independent holdout intervals before emitting an OMM. CelesTrak describes an analogous ephemeris-to-GP fitting approach for supplemental elements; SGP4 implementations should be checked against authoritative verification material. [R12, R13]

Availability of an SGP4 OMM for every object is a product objective, not a guarantee that every numerical trajectory has a useful SGP4 approximation. Preserve failure states and approximation errors. An OMM is an exchange message with declared theory, not evidence that a fitted orbit is accurate. The SDS binary representations used by SDN must retain the scientific semantics of their source OPM, OEM and OMM products; they are not the CCSDS XML wire format. [R14]

### 7.3 Two distinct conjunction baselines

The first baseline is an SGP4 compatibility replay against SOCRATES: identical input element editions where available, the same assessment interval, compatible model conventions, and explicit screening settings. CelesTrak's methodology identifies SGP4 and STK/CAT. Acquire required reports and GP records through the celestrak.eth test node, preserve their hashes, and record any unavailable proprietary settings. A live report matched to a later catalog is not a controlled comparison. [R15]

The second baseline evaluates the HPOP-selected catalog with independent orbit evidence and conjunction test cases. Report time-of-closest-approach and miss-distance discrepancies, missed and additional encounters, failure counts, screening bounds and uncertainty assumptions. A different HPOP catalog is not expected to reproduce every GP-derived SOCRATES encounter exactly. Collision probability additionally depends on calibrated relative-state uncertainty, cross-correlation assumptions, encounter geometry and hard-body radius. No live SOCRATES parity or collision-probability result is claimed by the present study.

## 8. Publication, feedback and the operator experience

### 8.1 Immutable distribution and incremental assessment

The proposed publication sequence is transactional: acquire bytes; verify integrity; normalize; validate; store and pin permitted source and derived products; publish a signed manifest; then announce the new catalog head. A conjunction worker should consume a complete immutable snapshot, not a mixture of partly updated providers. An update event should carry the object, previous and new solution references, effective epoch, recipe and provenance. Retries must be idempotent.

Content addressing, independent replication and open-source computation are mechanisms for resilience against unilateral data removal. They do not guarantee universal availability or erase provider access restrictions. Pin receipts should identify the actual nodes and retained bytes. Public notices may reference restricted products without publishing those products or credentials. A signed, readable module catalog is necessary for loading computation, but separate tests must demonstrate retrieval, invocation and resulting record publication.

The intended UT Austin node runs conjunction assessment as one service instance. Other nodes should be able to run the same open, auditable modules against the same authorized snapshots. Current availability, placement and throughput require live verification; the three-case offline experiment does not establish them.

### 8.2 Feedback as new evidence

Incoming observations should append evidence, trigger reevaluation of affected associations and solutions, and identify which conjunction results were superseded. Corrections must not silently overwrite provider originals. A proposed feedback record includes the disputed solution, supporting observations, proposed correction, relevant module/policy versions and disposition. Maintain separate reasons for malformed input, stale data, maneuver, incompatible model and unresolved identity.

### 8.3 Catalog and Space Aware interface

The proposed Catalog workspace presents ordered source layers, source coverage, freshness and integrity status, followed by a paginated object table. Each object opens a comparison showing identities, original and fitted solutions, frame/time conventions, residuals, uncertainty status and attribution. A review action should expose why a match is ambiguous and exactly what accepting it will change.

The core free orbital console is intended to expose catalog browsing, HPOP propagation, conjunction assessment and visualization through the same module artifacts. A result should link to its immutable inputs and recipe. Users should be able to distinguish “module available,” “calculation completed,” and “validated result.” Multi-page help should explain native versus derived states, association policy, holdouts and uncertainty. These interface and packaging requirements describe the intended experience; this paper is not a UI completion or release claim.

## 9. Acceptance plan and reproducibility

### 9.1 Operational acceptance gates

| Gate | Required evidence | Status in this study |
| --- | --- | --- |
| Acquisition | Exact bytes, source edition, integrity and archive lineage | Vimpel snapshots available; historical public-fleet transport evidence |
| Normalization | Identity, epoch, units, model and frame preserved | 13,808 Vimpel rows converted |
| Numerical refinement | Separate training/holdout metrics and rejected failures | Three four-hour cases demonstrated |
| Multi-provider identity | datefirst/AOE candidates, conflict review, accepted-link persistence | Incomplete |
| Uncertainty | Independent calibration and covariance applicability | Not established |
| Continuous network flow | Update, pin, stream, restart/replay and independent-node retrieval | Not established by this offline study |
| Conjunction verification | Controlled SOCRATES replay and independent HPOP tests | Pending |

The next study should stratify objects by orbital regime, eccentricity, source age, arc duration, observation availability and maneuver status. Preserve a final untouched validation set; repeated tuning against the same holdout set weakens its independence. Include parser failures and unmatchable objects in the denominator. Report failures and coverage alongside residual percentiles.

### 9.2 Implementation and software checks

The numerical evidence baseline is public modules commit 49e159d7003cac3d3e65b03170f11909fa86dd2c: files/orbit-products 0.1.1 and analysis/catalog-composer 0.1.8. The recorded package suites passed 68 tests. One opt-in parity test was run separately; one full-DE440s external-fixture test was skipped. Nine explicit parity cases exercised Chromium, native WasmEdge and the SDK container, with 45 comparisons. Required repository gates passed; an advisory repository-wide gate was blocked under machine overload. [R1]

The parity result checks selected identical-artifact behavior across runtimes. It does not validate every input or replace independent numerical evidence. Documentation changes after the baseline do not change the recorded numerical artifacts.

### 9.3 Immutable evidence identifiers

| Item | SHA-256 |
| --- | --- |
| Acquired Vimpel element table | 840cda6d499028c17a7e22228b1b22fe500078e996113ef5804c4fff7ba612fa |
| Acquired Vimpel ephemeris archive | f46df9c310a8c33752d5baebf429a0583e7896f6714501b77d8fcc033c65e6bb |
| Normalizer WASM | d9f8ea296eab142767a02eddcdc888899fdd8fdccd968a1b82479ff673698e82 |
| Catalog WASM | 7d87d552215685ee6a069f42f83d411af244e69d3cab7b4880f2e378cae30eed |
| HPOP WASM | 7305c5ef6db04cfb5babc4f2c5f87b17b1e5901e4c14bd25cf9ca7b14b7b48b1 |

Reproduce the offline study using the recorded commit, documented dependencies, exact artifact hashes and authorized copies of the source files. The driver is analysis/catalog-composer/tests/vimpel-live.mjs ELEMENTS_FILE EPHEMERIS_RAR. It routes existing C++/WASM reader, frame, time, propagation and fitting modules; it does not fetch provider data, install modules, publish records or create covariance. Detailed controls, per-case reference hashes and test commands are in the evidence record. [R1]

## 10. References

Project references are pinned to the numerical baseline where possible. External provider pages were reviewed on 21 September 2026; live pages may change. The Vimpel public bulletin was preserved and inspected during the underlying study; a fresh fetch was unavailable during this paper's preparation. No restricted raw records or credentials are included.

[R1] Digital Arsenal. Epoch-state conversion, validation and refinement; accompanying aggregate verification record. Modules commit 49e159d7003cac3d3e65b03170f11909fa86dd2c. [Method](https://github.com/DigitalArsenal/space-data-network-modules/blob/49e159d7003cac3d3e65b03170f11909fa86dd2c/analysis/catalog-composer/docs/epoch-fitting.md); [verification JSON](https://github.com/DigitalArsenal/space-data-network-modules/blob/49e159d7003cac3d3e65b03170f11909fa86dd2c/analysis/catalog-composer/docs/verification-vimpel-epoch-fit-20260921.json).

[R2] Digital Arsenal. Catalog Editor module: composition, coverage and matching contracts. Same baseline commit. [README](https://github.com/DigitalArsenal/space-data-network-modules/blob/49e159d7003cac3d3e65b03170f11909fa86dd2c/analysis/catalog-composer/README.md).

[R3] Digital Arsenal. Vimpel epoch normalization and catalog matching; 64-object diagnostic audit. Same baseline commit. [Normalization](https://github.com/DigitalArsenal/space-data-network-modules/blob/49e159d7003cac3d3e65b03170f11909fa86dd2c/analysis/catalog-composer/docs/vimpel-normalization.md); [audit JSON](https://github.com/DigitalArsenal/space-data-network-modules/blob/49e159d7003cac3d3e65b03170f11909fa86dd2c/analysis/catalog-composer/docs/vimpel-epoch-audit-20260921.json).

[R4] Space Mapper. AOE Catalog. [Provider catalog](https://spacemapper.cn/en-us/catalog/aoecat/).

[R5] Space Mapper. Orbital Database: standard, AOE and international product families. [Provider orbital products](https://spacemapper.cn/en-us/satellite/orbital).

[R6] Space Mapper. AOE orbit-list API documentation. Channel, response-format and metadata examples. [API documentation](https://spacemapper.cn/help/helpinfo/1613/).

[R7] Digital Arsenal. Ephemeris provider test fleet, historical development verification. Stack snapshot 43f5506457. [Provider-fleet report](https://github.com/DigitalArsenal/spacedatanetwork-stack/blob/43f5506457/studies/orbital-console-data/deployment/EPHEMERIS-PROVIDERS.md). Describes transport evidence and acquisition limitations; not independent orbit-accuracy evidence.

[R8] International Laser Ranging Service, NASA GSFC. Consolidated Prediction Format, v2. [CPF specification and supporting material](https://ilrs.gsfc.nasa.gov/data_and_products/formats/cpf.html).

[R9] Space Mapper. Space-object API documentation. AOE/INTL/MIXED sources and native/international identifiers. [Catalog API documentation](https://spacemapper.cn/help/helpinfo/1621/).

[R10] JSC Vimpel and Keldysh Institute of Applied Mathematics. Orbit parameters of newly detected HEO space debris objects; public bulletin and format explanation. [Provider bulletin](https://spacedata.vimpel.ru/en/). Interpretation and retained study evidence are documented in R3.

[R11] Petit, G. and Luzum, B., eds. IERS Conventions (2010), IERS Technical Note 36. [Conventions Centre](https://iers-conventions.obspm.fr/); [technical note](https://iers-conventions.obspm.fr/conventions/content/tn36.pdf).

[R12] CelesTrak. Current Supplemental GP Element Sets: methodology for fitting operator ephemerides with SGP4. [Methodology](https://www.celestrak.org/NORAD/elements/supplemental/). This reference is documentation, not a local GP-data download.

[R13] Vallado, D. A., Crawford, P., Hujsak, R. and Kelso, T. S. Revisiting Spacetrack Report #3. AIAA 2006-6753. [Paper and SGP4 verification foundation](https://www.celestrak.org/publications/aiaa/2006-6753/AIAA-2006-6753.pdf).

[R14] Consultative Committee for Space Data Systems. Orbit Data Messages, CCSDS 502.0-B-3, Issue 3, May 2023, including listed corrigenda. [Official publication record](https://ccsds.org/publications/allpubs/entry/3073/).

[R15] CelesTrak. SOCRATES Plus: methodology and service description. [SOCRATES methodology](https://celestrak.org/SOCRATES/). Consulted as a methodological reference; no local catalog/report acquisition or completed SDN parity claim is made.

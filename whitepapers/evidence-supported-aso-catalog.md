# Evidence-Supported ASO Catalog

An attributed orbital catalog for Space Data Network

**Anthony "TJ" Koury III and Dr. Moriba Jah**

Technical whitepaper 1.1 | Revised 24 September 2026  
Numerical evidence cutoff: 21 September 2026

## Contents

- [Executive summary](#executive-summary)
- [1 Evidence and the five contributors](#1-evidence-and-the-five-contributors)
- [2 Architecture and reproducible authority](#2-architecture-and-reproducible-authority)
- [3 Data inventory and evidence boundaries](#3-data-inventory-and-evidence-boundaries)
- [4 Normalization and epoch state physics](#4-normalization-and-epoch-state-physics)
- [5 Initializing numerical propagation from a TLE](#5-initializing-numerical-propagation-from-a-tle)
- [6 Estimation and model transfer validation](#6-estimation-and-model-transfer-validation)
- [7 Object association and solution selection](#7-object-association-and-solution-selection)
- [8 Dynamics and measurement models](#8-dynamics-and-measurement-models)
- [9 Inference and supported uncertainty](#9-inference-and-supported-uncertainty)
- [10 The measured Vimpel refinement study](#10-the-measured-vimpel-refinement-study)
- [11 Chinese AOE catalog integration](#11-chinese-aoe-catalog-integration)
- [12 Conjunction assessment and uncertainty](#12-conjunction-assessment-and-uncertainty)
- [13 Publication and the operator experience](#13-publication-and-the-operator-experience)
- [14 Acceptance gates and study design](#14-acceptance-gates-and-study-design)
- [15 Reproducibility and evidence baseline](#15-reproducibility-and-evidence-baseline)
- [References](#references)

## Executive summary

Space Data Network (SDN) is developing a reproducible, multi-provider catalog of anthropogenic space objects (ASOs) for distributed data sharing, orbit prediction, and conjunction assessment. We select solutions according to the evidence supporting each object, the intended use, and the prediction interval. Numerical high-precision orbit propagation (HPOP) is a principal computational capability; its value depends on the initial state, force model, measurement information, and treatment of uncertainty.

The catalog preserves original provider products and records how each selected or derived solution was obtained. It distinguishes direct observations from provider estimates, identifies shared source lineage, and makes model assumptions and unresolved parameters visible. Versioned dynamics and measurement models, observability reports, and explicit uncertainty representations extend the catalog's existing commitments to attribution and bounded claims.

A TLE evaluated through SGP4 at its epoch yields an estimated Cartesian state that can initialize numerical propagation after a proper frame transformation. This handoff is a legitimate computational operation. Its predictive benefit is an empirical question: errors in the estimated epoch state affect both propagation paths, while different dynamics change how those errors evolve. Neither retaining SGP4 nor switching to a numerical model guarantees the more accurate forecast.

The recorded Vimpel study converted all 13,808 acquired element rows and reduced withheld-position RMS discrepancies from 6.04–10.71 km to 52.82–69.93 m on three four-hour arcs. These are bounded consistency and model-reconciliation results against a provider ephemeris. Independent absolute accuracy, calibrated catalog-wide uncertainty, and operational collision probabilities remain to be established. [R1](#r1)–[R3](#r3)

This edition retains the numerical evidence baseline of 21 September 2026. It adds a validation path for TLE-seeded numerical propagation, object-specific modeling and measurement requirements, and proposed admissible-set inference and screening. These additions describe requirements and research directions; they do not add completed experiments to the evidence record.

## 1 Evidence and the five contributors

An orbit solution is an inference about a physical object. Its evidential support depends on five contributors. The catalog records the models and methods we choose, the measurements and provider products available to us, and the limits of what those inputs establish.

| No. | Contributor | Role in a catalog solution |
| --- | --- | --- |
| 1 | Actual physics | The forces and behavior experienced by the object; known only incompletely through evidence. |
| 2 | Dynamics model | Our mathematical description of motion, including shared physical models and object-specific parameters. |
| 3 | Observations | Measurements such as angles, ranges, range rates, and photometry, with acquisition and processing provenance. |
| 4 | Measurement model | The mapping from object and sensor states to predicted measurements, including timing, calibration, and biases. |
| 5 | Inference method | The procedure and assumptions used to estimate states, parameters, associations, and uncertainty. |

### Observations and provider estimates

A provider ephemeris or element set already incorporates that provider's dynamics model, measurement model, and inference. We call it a provider claim: an attributed estimate with an evidentiary history. This classification does not imply low quality. An operator solution with strong independent support may be the best available product. A direct observation also has instrument, processing, and error characteristics that must be documented.

Independence depends on lineage. Products published through different channels may reuse the same measurements or solutions. Agreement among such products is useful consistency evidence but cannot be counted as repeated independent confirmation. Unknown lineage remains an explicit uncertainty.

### Observability and observation design

Observability describes which state and physical parameters the available measurements can constrain, and at what precision. The report should distinguish structural ambiguity from weak sensitivity or inadequate geometry. A solution may constrain position well while leaving drag, radiation pressure, or attitude poorly separated.

We choose dynamics models, measurement models, and inference methods. We can also influence which observations become available through tasking, viewing geometry, sensor type, cadence, and arc length. The catalog should identify useful additional measurements and send those needs to participating sensors when tasking is available. Measurement outcomes themselves are not under our control.

## 2 Architecture and reproducible authority

Association, selection, and estimation remain separate decisions. Association asks whether records describe the same object. Selection chooses a usable solution for an epoch, interval, and purpose. Estimation derives a new solution under a declared model. Success in one stage does not establish success in the others.

The processing sequence is acquisition, preservation, evidence classification, normalization, association testing, selection or estimation, validation, publication, and conjunction assessment. Observability gaps and encounter uncertainty feed back into observation requests. Unresolved identities and failed estimates remain visible rather than being forced into the catalog.

### Record contracts

A source record retains provider, native identifier, edition, source hash, product type, epoch, time scale, frame, data, and lineage. Observation time, solution epoch, issue time, and retrieval time are distinct. An association records the source records, verdict, decision policy, and supporting evidence. A catalog snapshot binds its parent, recipe, association set, selected solutions, and source editions to immutable inputs.

| Solution field | Required meaning |
| --- | --- |
| `stateRef` and validity | The selected or derived state, reference epoch, and applicable interval. |
| `evidenceRefs` and lineage | Observations and provider claims, their classifications, dependencies, and source editions. |
| `dynamicsModelRef` | Force model, environmental inputs, and parameters marked estimated, bounded, or assumed. |
| `measurementModelRef` | Sensor geometry, timing, calibration, bias treatment, and known or undisclosed provider modeling. |
| `inferenceRef` | Estimation or selection method, weights, priors, assumptions, and software version. |
| `observabilityReport` | Constrained and unresolved parameters and the evidence supporting that assessment. |
| `uncertaintyStatus` | Available covariance, admissible-set bounds or reference, calibration evidence, and limitations. |

These fields define a logical contract, not a claim of complete implementation in an SDS schema. Authority is reproducible within a declared policy: identical inputs, module artifacts, and policy should yield the same decisions within stated numerical tolerances. Hashes establish byte identity and signatures establish attribution; physical validity requires separate evidence.

The implemented Catalog Editor composes immutable source editions and preserves selected record bytes. Its matcher evaluates proposed associations without automatically publishing identity merges. End-to-end orchestration and the added evidence and uncertainty contracts remain integration work. [R1](#r1), [R2](#r2)

## 3 Data inventory and evidence boundaries

| Source family | Class | Evidence and treatment |
| --- | --- | --- |
| Vimpel elements | Orbit claim | 13,808 acquired rows converted to attributed epoch OPM states. Preserve native identity and provider uncertainty statements. |
| Vimpel ephemerides | Orbit claim | Archive audit found 6,654 members matching elements by native identity and exact epoch. A 64-object diagnostic and three-object refinement study were recorded. |
| Vimpel datefirst and aliases | Identity claim | Supporting association evidence. Automatic candidate generation and accepted-binding persistence remain incomplete. |
| CelesTrak GP and SOCRATES | Orbit and assessment claims | Native-model interoperability and controlled conjunction comparison. No completed SOCRATES agreement result is asserted. |
| Space Mapper AOE | Orbit claim | Documented proposed integration. No acquired catalog snapshot or numerical validation in the baseline. |
| Operator and precise products | Orbit claim | Potential primary or reference solutions, depending on quality, independence, normalization, and maneuver information. |
| CPF predictions | Orbit claim | Prediction products; their format does not make them raw tracking observations. |
| Sensor measurements | Observation | Angles, ranges, range rates, and photometry can support estimation when their measurement models and error characteristics are available. |

The historical provider-fleet report records 13 public-source lanes passing signed publication, independent IPFS retrieval, pagination, and byte-hash checks: SpaceX Starlink, Eutelsat OneWeb, Planet Labs, NASA ISS, SES, Intelsat, Telesat, China Space Station, IGS/BKG GPS Precise, ESA GLONASS Precise, ESA Precise Orbit Determination, EUMETSAT, and ESA CPF Predictions. These development instances were named for their sources; they were not services operated by those organizations. [R7](#r7)

Acquisition was incomplete in some lanes: 38 of 41 SES resources were recorded and Starlink acquisition was continuing. Authenticated Spire Global, Space-Track, EDC CPF, and Vimpel adapters were listed. Later Vimpel acquisition evidence supersedes that report's then-unverified Vimpel status without changing the status of other sources. Transport checks do not establish orbit accuracy or current service availability. [R2](#r2), [R3](#r3), [R7](#r7)

Each edition retains format version, interval, access restrictions, retrieval result, and upstream lineage. Authorized raw measurements are a development priority because they support direct testing of object and sensor models. When only provider estimates are available, the catalog can still compare, select, and reconcile them with explicit limits. [R8](#r8)

## 4 Normalization and epoch state physics

### Vimpel osculating elements

Vimpel's documented 15-column product includes native object number, first observation date, UTC reference epoch, update age, semimajor axis, inclination, ascending-node longitude, eccentricity, argument of latitude, argument of perigee, effective area-to-mass, magnitude, and two uncertainty indicators. Angles are in degrees, semimajor axis is in kilometres, and the stated frame is J2000. This is a provider-specific osculating product. [R3](#r3), [R10](#r10)

At the reference epoch, true anomaly is the argument of latitude minus the argument of perigee. With consistent angles, Earth gravitational parameter $\mu$, eccentricity $e$, and semimajor axis $a$, the conversion is:

$$
\nu = u - \omega, \qquad p = a(1-e^2)
$$

$$
\mathbf{r}_{\mathrm{pf}} = \frac{p}{1+e\cos\nu}\begin{bmatrix}\cos\nu \\ \sin\nu \\ 0\end{bmatrix}
$$

$$
\mathbf{v}_{\mathrm{pf}} = \sqrt{\frac{\mu}{p}}\begin{bmatrix}-\sin\nu \\ e+\cos\nu \\ 0\end{bmatrix}
$$

$$
Q = R_z(\Omega)\,R_x(i)\,R_z(\omega)
$$

$$
\mathbf{r}_{\mathrm{J2000}} = Q\mathbf{r}_{\mathrm{pf}}, \qquad \mathbf{v}_{\mathrm{J2000}} = Q\mathbf{v}_{\mathrm{pf}}
$$

Here $Q$ rotates the perifocal vectors using ascending-node longitude $\Omega$, inclination $i$, and argument of perigee $\omega$. The implemented reader uses $\mu = 398600.4418\,\mathrm{km}^3/\mathrm{s}^2$, explicit internal SI conversion, and canonical OPM output in km and km/s. It retains `vimpel:<nativeID>` and the source descriptor without fabricating a NORAD identifier or international designator. This is an instantaneous conversion, not propagation to another epoch. [R1](#r1)–[R3](#r3)

### Position samples and velocity

Numerical differentiation of provider positions is a diagnostic, with error that depends on spacing, rounding, stencil, and orbital geometry. At the archive's 600-second spacing, the 64-object audit found differences up to about 1.106 km/s between analytic and endpoint-derived velocities, and up to 1.824 km/s between derivative stencils. These discrepancies rule out unconditional use of the endpoint derivative, but do not determine which product is absolutely accurate. [R3](#r3)

The preferred initial velocity is the analytic osculating velocity or a validated fitted state. Position-only evidence remains position-only; a missing velocity is not replaced by zero. Reducing a differentiation step does not necessarily improve an estimate when timestamps and positions have finite precision.

### Frames and time

Transformations identify conventions and supporting data. The recorded Vimpel path transforms J2000 to GCRF for HPOP and back for comparison, and UTC sample times to TDB for propagation. TEME, terrestrial frames, J2000 conventions, and GCRF are distinguished explicitly. Earth-orientation, leap-second, and ephemeris inputs must be versioned where required. [R1](#r1), [R11](#r11)

## 5 Initializing numerical propagation from a TLE

A TLE is a set of model-specific mean elements. Evaluating it with SGP4 at its reference epoch produces SGP4's estimated instantaneous Cartesian position and velocity. After a consistent frame transformation, that state can serve as the initial condition for numerical integration. We distinguish the validity of this initialization from the empirical accuracy of the resulting forecast. [R13](#r13), [R16](#r16)

### The handoff contract

First, preserve the TLE bytes, epoch, identifiers, source edition, and SGP4 implementation and constants. Evaluate SGP4 at zero elapsed time, including the appropriate near-Earth or deep-space behavior. Use its returned position and velocity; do not treat the mean elements as ordinary osculating Keplerian elements.

Second, transform the complete state from TEME to the numerical integration frame at the same physical instant. The velocity transformation must include the frame's time dependence where required. Record the transformation conventions, supporting data, units, and time scales. Verify a round trip and confirm that the transformed state and numerical initial state agree within declared tolerances.

Third, declare the numerical force model, environmental inputs, integration settings, and object parameters. A TLE alone does not supply independently determined mass, attitude, drag coefficient, or radiation-pressure coefficient. Its $B^*$ parameter belongs to the SGP4 drag model; any mapping into a numerical drag model requires explicit assumptions and validation.

Finally, publish the resulting trajectory as a derived, TLE-seeded numerical product with its parent state and model lineage. Retain the native SGP4 product and identify the interval over which the numerical product has been assessed.

### What the handoff establishes

The two paths start from the same estimated position and velocity, expressed in a common frame. They subsequently evolve according to different dynamics. Applying numerical force terms after the handoff does not by itself double-count the perturbations used to obtain the epoch state: those terms govern subsequent evolution. Their adequacy and parameter values still require assessment.

A state error at epoch is an error for both paths. Continuing with SGP4 does not remove it, and switching models does not create it. Different models can nevertheless amplify, reduce, or partly compensate its effect on predicted positions. Neither model complexity nor model consistency alone decides which forecast is closer to the object.

The reported Vimpel study used osculating elements and provider ephemerides; it did not test this TLE handoff. No predictive advantage for TLE-seeded numerical propagation is claimed from that experiment.

## 6 Estimation and model transfer validation

### What an orbit fit optimizes

Orbit determination estimates parameters by comparing predicted measurements with observations over a fitting arc. In weighted least squares, the objective is a weighted residual sum, potentially with priors or regularization. It does not generally minimize the physical position and velocity error specifically at the solution epoch. Sensor geometry, measurement errors, force-model errors, and fitted parameters all influence the inferred epoch state. [R17](#r17)

Initial-state and dynamics errors can compensate within a fit. A velocity that is too high can partly compensate for an acceleration that is too low over the observation arc. Both remain errors in the original model. Replacing the acceleration model while keeping the estimated velocity changes that compensation; it can improve or degrade the forecast. Model dependence therefore motivates testing rather than a presumption that SGP4 or numerical propagation is superior. [R13](#r13)

Formal covariance describes uncertainty under the estimator's assumed models and error statistics. Minimizing measurement residuals is not equivalent to minimizing covariance, and a small formal covariance can coexist with systematic bias. A standard TLE does not itself provide a full state covariance. The catalog records what uncertainty was supplied, inferred, calibrated, or remains unknown.

### A controlled comparison

For each test object and TLE edition, evaluate native SGP4 and numerical propagation initialized from its epoch state over the same forecast interval. Use common frame and time conventions and an independent reference with documented accuracy, lineage, and maneuver treatment. Report reference uncertainty along with position and velocity errors, radial/along-track/cross-track components, maxima, percentiles, and error growth with prediction age.

Stratify by orbit regime, perigee altitude, eccentricity, source age, physical-parameter knowledge, maneuver status, and forecast length. Use the same test cases and include failures. Distinguish tests supplied with additional physical information from tests using only TLE information. Freeze models and tuning before evaluating an untouched forecast set.

### Optional fitting across an arc

A numerical initial state and identifiable parameters may be fitted to an SGP4-generated arc or a history of TLE-derived states. This can improve consistency across the transition. Research has demonstrated improved forecasts from fits to successive TLE-derived states in tested cases, but the result is not universal. Such fitting remains inference from provider estimates and may inherit their biases or correlations. [R18](#r18)

An arc fit is an optional model-transfer technique, not a prerequisite for initializing an integrator. When independent observations are available, direct estimation under the chosen numerical and measurement models offers a stronger basis for evaluating physical accuracy. The acceptance criterion is performance for the intended use, with supported uncertainty and transparent failures.

## 7 Object association and solution selection

### Candidate generation

Candidate associations combine provider crosswalks, international designators, native-identifier histories, and coarse orbital compatibility. Vimpel's datefirst fields `Nvym`, `t_det_v`, `Nnor`, and `t_det_n` supply attributed identity evidence. Preserve dates, leading-zero normalization, and contradictory or duplicate declarations. Names and unqualified numeric identifiers are not sufficient join keys. [R3](#r3)

Similar trajectories can describe formation members, recent deployments, or fragments. Pairwise compatibility is not automatically transitive. Proposed identity groups need component-wide conflict checks and a consistent one-to-one assignment where the catalog semantics require it. Unresolved records remain separate, and observations that fit no existing candidate retain an unassigned or new-object hypothesis.

### Physical compatibility

Compare trajectories on a common origin, frame, time scale, start epoch, and sampling grid over overlapping validity intervals. The implemented matcher accepts already normalized OEM inputs; the caller performs the required propagation and transformations. It assesses position, velocity, and derivative consistency against explicit thresholds. Its verdict is trajectory compatibility rather than automatic identity publication. [R2](#r2)

Report RMS and maximum discrepancies, duration, sample count, source epochs, and propagation ages. Require sufficient coverage rather than one coincident position. Treat maneuvers, discontinuities, and non-overlapping arcs explicitly. Thresholds belong to the versioned recipe and require calibration; future uncertainty-aware tests should retain their assumptions about correlation and admissible sets.

### Selecting a solution

For an accepted identity, exclude invalid or inapplicable products, then evaluate validity coverage, independent support, measurement and dynamics quality, maneuvers, uncertainty calibration, predictive performance, propagation age, source lineage, and declared source preference. Selection produces a source reference and an explanation appropriate to the requested use.

A numerical solution can be preferred when evidence supports its forecast, including a validated TLE-seeded solution. A native SGP4 or operator solution can be preferred when it is better supported. Freshness and propagator sophistication alone are insufficient ranking criteria. Position, velocity, and uncertainty must form a consistent solution; they are not spliced from unrelated estimates.

Review outcomes are compatible, rejected, ambiguous, or insufficient. Accepted links retain the reviewer or decision policy and evidence so later information can reverse an association without rewriting history. The current recipe-v2 editor uses international designators for authoritative composition. Objects without accepted designators remain review candidates even where the current CAT export excludes them. [R2](#r2)

## 8 Dynamics and measurement models

### Shared physics and object parameters

A dynamics model combines well-characterized shared physics, object-specific effects, and residual model error. A useful decomposition is:

$$
\ddot{\mathbf{r}} = \mathbf{a}_{\mathrm{gen}}(\mathbf{r},\dot{\mathbf{r}},t) + \mathbf{a}_{\mathrm{obj}}(\mathbf{r},\dot{\mathbf{r}},t;\mathbf{p}) + \boldsymbol{\delta}(t)
$$

The shared term includes the selected gravity, third-body, tidal, and relativistic models. The object term depends on parameters such as area-to-mass, drag and radiation response, attitude, and thrust. The residual term represents remaining model discrepancy, with declared bounds or an appropriate stochastic description.

Known force terms need not be estimated anew from every object's observations to be useful. Observability limits the estimation of unknown parameters and the confidence assigned to them; it does not prohibit using independently supported physics. An unresolved parameter may be bounded, marginalized, or assigned a documented prior. The choice of model complexity considers sensitivity, available evidence, and the forecast interval.

The dominant errors depend on orbit, altitude, object behavior, measurement quality, and prediction age. High eccentricity alone does not establish strong atmospheric drag; perigee altitude and atmospheric conditions also matter. Track parameter histories and model residuals across arcs. Suspected maneuvers trigger a validity review and, where appropriate, a new arc or a model with explicit maneuver uncertainty.

### Measurement models as versioned artifacts

A measurement model maps the object and sensor states to a predicted observable. It includes geometry, timing, calibration, frame conventions, and relevant corrections such as light-time, aberration, and atmospheric refraction. Bias, random error, and residual measurement-model discrepancy are represented distinctly:

$$
\mathbf{y} = h(\mathbf{x},\mathbf{p},\mathbf{s},t) + \mathbf{b} + \mathbf{e} + \boldsymbol{\varepsilon}
$$

Here $\mathbf{x}$ is the object state, $\mathbf{p}$ its physical parameters, $\mathbf{s}$ the sensor state and calibration, $\mathbf{b}$ the modeled bias, $\mathbf{e}$ random error, and $\boldsymbol{\varepsilon}$ residual model discrepancy. A missing or undisclosed provider measurement model is recorded as unknown.

Each contributing sensor should publish a versioned model with location, timing provenance, pointing and calibration history, error characterization, and known limitations. At an illustrative speed of 7.5 km/s, a 1 ms timing offset corresponds to 7.5 m of travel; its measurement impact depends on geometry. Photometric inference states its reflectance and attitude assumptions.

Estimate biases when the observations can constrain them, otherwise carry defensible bounds or priors. Independent reference trajectories and observations across multiple objects can help separate sensor errors from orbital model errors. Residuals alone generally do not identify which contributor caused a discrepancy.

## 9 Inference and supported uncertainty

The catalog supports estimation methods whose assumptions and uncertainty can be examined. Least squares, probabilistic filters, and bounded-set methods are evaluated according to their model treatment, calibration, computational cost, and performance. Least squares can include estimated biases, physical parameters, and model-error terms; a state-only fit is one particular configuration.

### Covariance and model discrepancy

Preserve provider uncertainty statements with their stated semantics. Vimpel's two 50%-confidence indicators do not define a full six-dimensional covariance, correlations, a Gaussian distribution, or hard bounds. A fitting regularizer and small residuals likewise do not supply a validated physical covariance. [R3](#r3), [R10](#r10)

A covariance product must specify its state, epoch, frame, distributional assumptions, measurement errors, dynamics, model discrepancy, and calibration evidence. Propagation may use a state-transition model and a declared process-noise contribution, but unvalidated tuning is not a substitute for independent coverage tests. Unknown cross-provider correlations preclude treating simple inverse-covariance averaging as automatically justified.

### Admissible trajectories

An admissible set contains states, parameters, and associated trajectories consistent with the evidence under explicitly declared assumptions and error allowances. Parameter ranges, measurement-error sets, and dynamics-discrepancy bounds define what can be excluded. Provider claims require their own uncertainty treatment and dependency information before acting as constraints.

Bounds are substantive modeling choices. When errors have unbounded tails, a finite envelope must state its coverage or truncation convention rather than claiming an absolute guarantee. New evidence can contract a fixed hypothesis set; propagation with process uncertainty can expand it. Maneuvers, revised bounds, or inadequate models can require reopening or expanding the hypotheses.

### Proposed TEAG and ESPF evaluation

We propose evaluating the Theory of Epistemic Abductive Geometry (TEAG) and the Epistemic Support-Point Filter (ESPF) as an implementation path for inference using admissible sets. Their support-point and bounding-geometry approach expresses plausibility through possibility and necessity and accommodates unresolved alternatives. Possibility describes compatibility with evidence; necessity describes support through exclusion of alternatives. These quantities are not collision probabilities. [R19](#r19), [R20](#r20)

The SDN evaluation must document bound construction, support resolution, treatment of outliers and inconsistent evidence, and behavior under model mismatch. It must compare against appropriate probabilistic and bounded-set baselines using independent cases. Published bounds must state whether they conservatively enclose the admissible trajectories or only summarize sampled support. TEAG/ESPF integration and catalog-scale validation are proposed work, not results of the Vimpel experiment.

## 10 The measured Vimpel refinement study

### Method and experimental configuration

The implemented refinement estimates a six-component initial-state correction using finite-difference sensitivities from one nominal and six positively perturbed trajectories. It checks initial-state bindings and requires six numerically independent sensitivity columns. These derivatives measure sensitivity to the initial state; they do not differentiate coarse provider positions to obtain velocity. [R1](#r1)

A regularized weighted least-squares update uses training positions. Corrections outside configured position and velocity scales are rejected. A candidate has stale Keplerian fields cleared and is returned as `candidate-needs-propagation`. Repropagation and validation are mandatory, with an optional prior withheld-RMS gate rejecting deterioration. The module does not emit a provider covariance or automatically promote a catalog solution. [R1](#r1)

Each of three four-hour arcs contained 25 positions at 600-second spacing. Withheld indices 3, 7, 11, 15, 19, and 23 left 19 training and six validation positions. One update was performed. The force model used central Earth gravity, J2–J4, analytical Sun/Moon perturbations, and RKF78 integration; drag and solar-radiation pressure were disabled. [R1](#r1)

| Eccentricity | Initial withheld RMS (km) | Fitted withheld RMS (m) | Maximum withheld residual (m) |
| --- | --- | --- | --- |
| 0.004351 | 10.70686 | 69.929 | 97.349 |
| 0.892913 | 6.04357 | 59.097 | 72.843 |
| 0.840641 | 10.41481 | 52.821 | 89.683 |

### Interpretation and next measurements

Both training and withheld maxima passed the example 1 km gate, which was experimental tuning rather than an operational safety threshold. Withheld samples were excluded from estimation but came from the same provider product. The results demonstrate software capability and short-arc consistency between models; they do not establish independent absolute accuracy or a representative catalog-wide error distribution.

Vimpel's published model includes degree-8 Earth gravity, DE405 Sun/Moon, atmosphere, and radiation pressure. The tested configuration differs and the current GOST selector is a placeholder. The initial 6–11 km discrepancies merit decomposition, but the study does not identify their individual causes. A state correction can absorb some model mismatch over four hours. [R1](#r1), [R3](#r3), [R10](#r10)

The next study should vary force terms and conventions systematically, report state corrections and sensitivity conditioning, assess longer predictions across the available ephemeris, and compare with independent reference products. Include failed cases and supported uncertainty envelopes. The original numerical results and their bounded interpretation remain unchanged.

## 11 Chinese AOE catalog integration

### Source and model identification

This design interprets the proposed Chinese catalog as Space Mapper's AOE catalog, subject to confirmation of the intended provider. Its documented families include self-determined AOE orbits, international multi-source orbits, and a combined product. The self-determined family's use of its parent company's observation network is a provider assertion to preserve and assess. [R4](#r4), [R5](#r5)

The documented orbit-list API uses bearer-token authentication and KY and INTERNATIONAL channels, with TLE, two-line TLE, JSON, and OMM-XML responses. Its OMM example declares Earth, TEME, UTC, and SGP4. A JSON example uses a timezone offset. The catalog API exposes MIXED, AOE, and INTL sources and distinguishes catalog identifiers from NORAD identifiers. Actual acquired responses must be checked against these examples. [R6](#r6), [R9](#r9)

### Adapter and provenance requirements

Maintain separate namespaces such as `spacemapper:KY` and `spacemapper:INTERNATIONAL`. Retain the requested channel, response metadata, native catalog identifier, asserted international links, epoch string, format, exact bytes, and upstream lineage. Normalize timezone-qualified epochs while preserving their original representation. Numeric identifiers are joined only with their namespaces and supporting association evidence.

An SGP4 mean-element product is evaluated through SGP4 before any numerical initialization or fit. It does not pass through the Vimpel osculating-element converter. Missing frame, time, or model metadata yields a validation failure or explicitly incomplete product. The TLE handoff and validation requirements in sections 5 and 6 apply to derived numerical solutions.

AOE, international, and mixed channels may share upstream information. Preserve a lineage graph and avoid counting duplicated inputs as independent evidence. A demonstrably independent KY estimate could be especially useful for testing agreement and disagreement across sources. Its value still depends on quality, geometry, uncertainty, and relevance to the intended interval; independence alone does not guarantee accuracy.

### Admission study

Before acquisition and combination, document applicable authorization, licensing, and any required export-control review. Acquire an authorized immutable snapshot, verify pagination and completeness, record counts and hashes, and test representative formats and orbital regimes. Compare time-aligned products and report coverage, consistency, conflicts, unknown lineage, and unavailable cases separately.

No acquired AOE coverage count, completed integration, improved conjunction accuracy, or legal conclusion is asserted here. The historically tested China Space Station operator feed is a separate source and provides no evidence of AOE catalog ingestion.

## 12 Conjunction assessment and uncertainty

### Two validation baselines

The first baseline is a controlled SGP4 replay against SOCRATES using identical input editions where available, the same assessment interval, compatible model conventions, and explicit screening settings. CelesTrak identifies SGP4 and STK/CAT in its methodology. The designated acquisition path remains the celestrak.eth test node. Preserve catalog and report hashes and record unavailable proprietary settings. A live report compared with a later catalog is not a controlled replay. [R15](#r15)

The second baseline assesses the selected catalog, including numerical and other supported solutions, against independent orbit evidence and conjunction cases. Report closest-approach time and miss-distance errors, missed and additional encounters, coverage, failures, screening bounds, and uncertainty assumptions. A catalog using different orbital evidence or dynamics need not reproduce every GP-derived SOCRATES event.

### Screening admissible trajectories

For two objects, propagate their admissible trajectory sets over a common interval. An encounter is geometrically possible under the declared bounds if some jointly admissible pair comes within the combined hard-body radius at the same time. Where the objects share errors or evidence, joint admissibility must account for those dependencies rather than automatically taking every combination of marginal states.

A conservative enclosing bound can support exclusion when it remains separated from the collision region. Intersection of conservative envelopes may be only a candidate requiring refinement, since an envelope can contain states that no admissible trajectory reaches. Finite support-point samples alone cannot guarantee that all encounters have been screened; coverage and enclosure assumptions must be explicit.

Possibility and necessity scores require a specified possibility model and event definition. A geometric intersection by itself supplies no calibrated probability or graded score. In a normalized possibility model, necessity is related to the possibility of the complementary event. Report the definitions used and avoid interpreting a low necessity as proof of safety. [R19](#r19), [R20](#r20)

### Operator reporting and tasking

When supported, report ranges of closest-approach times and miss distances and identify whether initial-state uncertainty, unresolved object parameters, dynamics error, or measurement error dominates the encounter. Those findings should identify observations that could resolve an ambiguity. Wider uncertainty can increase screening workload; acceptance testing must measure missed encounters, false alerts, and computational cost.

Collision probability remains available when the relative-state uncertainty model has suitable independent calibration and states its cross-correlation, geometry, and hard-body-radius assumptions. No live SOCRATES parity, validated admissible-set screening, or collision-probability result is claimed by the present evidence baseline.

## 13 Publication and the operator experience

### Immutable distribution

Publication proceeds transactionally: acquire and preserve bytes, verify integrity, normalize and validate, retain permitted source and derived products, publish a signed manifest, then announce the new catalog head. A conjunction worker consumes a complete immutable snapshot. Update events identify the object, prior and new solution references, effective epoch, recipe, and provenance. Retries are idempotent.

Content addressing, independent replication, and open computation support resilience. They do not guarantee availability or override provider restrictions. Pin receipts identify retained bytes and actual nodes. A public notice may reference a restricted product without publishing its contents or credentials. Module availability, successful invocation, and validated output publication are separate operational checks.

The intended UT Austin conjunction service is one placement of an auditable service that other authorized nodes can reproduce. Its availability, placement, throughput, and restart behavior require live verification. The recorded offline study does not establish operational readiness.

### Feedback and reversible decisions

New observations append evidence and trigger reevaluation of affected identities, solutions, and conjunctions. Corrections retain the provider originals and indicate which derived results are superseded. A feedback record identifies the disputed solution, supporting evidence, proposed correction, module and policy versions, and disposition. It distinguishes malformed inputs, stale data, maneuvers, incompatible models, and unresolved identity.

Model revisions and changed error bounds are versioned events. A trajectory excluded under one model may become admissible under a corrected model; history must preserve why the earlier decision was made. New-object and unassigned hypotheses remain available where current explanations are inadequate.

### Catalog and Space Aware interface

The proposed workspace presents ordered source layers, coverage, freshness, integrity, and a paginated object table. An object comparison exposes native and derived solutions, evidence classifications, lineage, model versions, frame and time conventions, residuals, uncertainty status, and selection reasons. Observability gaps and admissible-set limits should be visible alongside a representative trajectory.

A review action explains the ambiguity and the effects of accepting an identity link or solution. Every calculation links to immutable inputs and a recipe. The intended free orbital console exposes catalog browsing, propagation, conjunction assessment, and visualization through the same module artifacts. Help distinguishes native products, TLE-seeded numerical products, fitted solutions, consistency validation, and independent accuracy validation. These remain interface and packaging requirements rather than release claims.

### SGP4 companion products

An accepted numerical solution may be sampled and fitted with SGP4 mean elements over a declared interval, with separate fit and holdout checks before an OMM is emitted. Approximation errors and failures remain visible. An SGP4 companion for every object is a product objective, not a guaranteed useful approximation. SDS representations retain source OPM, OEM, and OMM semantics without claiming to be the CCSDS XML wire format. [R12](#r12)–[R14](#r14)

## 14 Acceptance gates and study design

| Gate | Required evidence | Recorded status |
| --- | --- | --- |
| Acquisition | Exact bytes, source edition, integrity, permitted retention, and lineage. | Vimpel snapshots and historical fleet transport checks. |
| Normalization | Preserved identity, epoch, units, frame, time scale, and model semantics. | 13,808 Vimpel rows converted. |
| Evidence classification | Observations and provider claims tagged with dependencies and unknown lineage. | Expanded contract proposed. |
| Model reconciliation | Training and withheld residuals, state shifts, sensitivity checks, and failure counts. | Three four-hour cases; decomposition pending. |
| TLE model transfer | Verified epoch handoff and comparative forecasts against independent references. | Not established by the Vimpel study. |
| Object and sensor models | Versioned models, parameter observability, timing and bias treatment, and declared assumptions. | Expanded requirements proposed. |
| Physical accuracy | Independent reference comparisons stratified by regime and prediction interval. | Not established. |
| Uncertainty | Calibration of covariance or coverage and conservatism of declared trajectory bounds. | Not established. |
| Identity | Candidate review, conflict checks, reversible accepted links, and unassigned cases. | Incomplete. |
| Network flow | Update, pin, stream, restart/replay, and independent retrieval of complete snapshots. | Continuous operation not established by offline study. |
| Conjunction | Controlled SOCRATES replay and independent assessment of selected trajectories and uncertainty. | Pending. |

The next study stratifies objects by orbit regime, perigee altitude, eccentricity, source age, arc duration, observation availability, maneuver status, parameter observability, and independent-reference availability. Report coverage and failure rates alongside residual percentiles, and retain parser failures and unmatchable objects in the denominator.

Reserve a final untouched validation set. Repeated tuning against withheld points turns those points into development data. Assess the benefits of additional observations and physical information separately from benefits due only to a change in propagation model. Results should support a declared use and interval rather than a universal ranking of propagators.

## 15 Reproducibility and evidence baseline

The numerical baseline is modules commit `49e159d7003cac3d3e65b03170f11909fa86dd2c`, including `files/orbit-products` 0.1.1 and analysis/catalog-composer 0.1.8. The recorded package suites passed 68 tests. One opt-in parity test ran separately; one full-DE440s external-fixture test was skipped. Nine explicit parity cases exercised Chromium, native WasmEdge, and the SDK container with 45 comparisons. Required repository gates passed; an advisory repository-wide gate was blocked under machine overload. [R1](#r1)

These checks establish selected software behavior across runtimes. They do not validate all inputs, establish independent orbit accuracy, or demonstrate operational readiness. The implementation statuses and measured results in this edition refer to that baseline; the revised architecture does not imply that the new requirements have been implemented.

### Immutable evidence identifiers

**Acquired Vimpel element table**

```text
840cda6d499028c17a7e22228b1b22fe500078e996113ef5804c4fff7ba612fa
```

**Acquired Vimpel ephemeris archive**

```text
f46df9c310a8c33752d5baebf429a0583e7896f6714501b77d8fcc033c65e6bb
```

**Normalizer WASM**

```text
d9f8ea296eab142767a02eddcdc888899fdd8fdccd968a1b82479ff673698e82
```

**Catalog WASM**

```text
7d87d552215685ee6a069f42f83d411af244e69d3cab7b4880f2e378cae30eed
```

**HPOP WASM**

```text
7305c5ef6db04cfb5babc4f2c5f87b17b1e5901e4c14bd25cf9ca7b14b7b48b1
```

### Reproduction contract

Use the recorded commit, documented dependencies, exact artifact hashes, and authorized source files. The driver is `analysis/catalog-composer/tests/vimpel-live.mjs` with `ELEMENTS_FILE` and `EPHEMERIS_RAR` arguments. It routes existing C++/WASM reader, frame, time, propagation, and fitting modules. It does not acquire provider data, install modules, publish records, or create covariance. Per-case reference hashes and controls remain in the verification record. [R1](#r1)

Reproduction of the new TLE-transfer and uncertainty studies additionally requires frozen test editions, independent-reference lineage, force and measurement models, parameter assumptions, training and validation partitions, and prediction intervals. Report unavailable inputs and rejected cases rather than silently substituting a model or source.

## References

R1–R15 retain the sources and evidence roles of the 21 September 2026 baseline. Provider API descriptions refer to that reviewed baseline, not a fresh acquisition. R16–R20 support the added discussion of model transfer, estimation, and proposed inference methods. Restricted source records and credentials are not reproduced.

### R1

Digital Arsenal. Epoch-state conversion, validation and refinement, with aggregate verification record. Modules commit `49e159d7003cac3d3e65b03170f11909fa86dd2c`.

[Method](https://github.com/DigitalArsenal/space-data-network-modules/blob/49e159d7003cac3d3e65b03170f11909fa86dd2c/analysis/catalog-composer/docs/epoch-fitting.md) · [Verification record](https://github.com/DigitalArsenal/space-data-network-modules/blob/49e159d7003cac3d3e65b03170f11909fa86dd2c/analysis/catalog-composer/docs/verification-vimpel-epoch-fit-20260921.json)

### R2

Digital Arsenal. Catalog Editor module: composition, coverage and matching contracts. Same baseline commit.

[Module documentation](https://github.com/DigitalArsenal/space-data-network-modules/blob/49e159d7003cac3d3e65b03170f11909fa86dd2c/analysis/catalog-composer/README.md)

### R3

Digital Arsenal. Vimpel epoch normalization and catalog matching; 64-object diagnostic audit. Same baseline commit.

[Normalization](https://github.com/DigitalArsenal/space-data-network-modules/blob/49e159d7003cac3d3e65b03170f11909fa86dd2c/analysis/catalog-composer/docs/vimpel-normalization.md) · [Audit record](https://github.com/DigitalArsenal/space-data-network-modules/blob/49e159d7003cac3d3e65b03170f11909fa86dd2c/analysis/catalog-composer/docs/vimpel-epoch-audit-20260921.json)

### R4

Space Mapper. AOE Catalog. Provider description.

[AOE Catalog](https://spacemapper.cn/en-us/catalog/aoecat/)

### R5

Space Mapper. Orbital Database. Standard, AOE, and international product families.

[Orbital products](https://spacemapper.cn/en-us/satellite/orbital)

### R6

Space Mapper. Orbit-list API documentation. Channels, formats, and metadata examples.

[Orbit API](https://spacemapper.cn/help/helpinfo/1613/)

### R7

Digital Arsenal. Ephemeris provider test fleet. Historical development verification at stack snapshot 43f5506457; transport evidence and acquisition limitations.

[Provider-fleet report](https://github.com/DigitalArsenal/spacedatanetwork-stack/blob/43f5506457/studies/orbital-console-data/deployment/EPHEMERIS-PROVIDERS.md)

### R8

International Laser Ranging Service, NASA GSFC. Consolidated Prediction Format, version 2, and supporting material.

[CPF documentation](https://ilrs.gsfc.nasa.gov/data_and_products/formats/cpf.html)

### R9

Space Mapper. Space-object API documentation. AOE, INTL, and MIXED sources and identifiers.

[Catalog API](https://spacemapper.cn/help/helpinfo/1621/)

### R10

JSC Vimpel and Keldysh Institute of Applied Mathematics. Orbit parameters of newly detected HEO space debris objects. Public bulletin and format explanation; retained study interpretation in R3.

[Provider bulletin](https://spacedata.vimpel.ru/en/)

### R11

Petit, G., and Luzum, B., editors. IERS Conventions (2010). IERS Technical Note 36.

[Technical note](https://iers-conventions.obspm.fr/conventions/content/tn36.pdf)

### R12

CelesTrak. Current Supplemental GP Element Sets. Methodology for fitting operator ephemerides with SGP4.

[Supplemental GP methodology](https://www.celestrak.org/NORAD/elements/supplemental/)

### R13

Vallado, D. A., Crawford, P., Hujsak, R., and Kelso, T. S. Revisiting Spacetrack Report #3. AIAA 2006-6753. SGP4 theory, verification, frame conventions, and model-dependent estimation cautions.

[Paper](https://celestrak.org/publications/AIAA/2006-6753/AIAA-2006-6753.pdf)

### R14

Consultative Committee for Space Data Systems. Orbit Data Messages. CCSDS 502.0-B-3, Issue 3, May 2023, including listed corrigenda.

[Publication record](https://ccsds.org/publications/allpubs/entry/3073/)

### R15

CelesTrak. SOCRATES Plus. Methodology and service description; cited as documentation, not a completed SDN parity result.

[SOCRATES methodology](https://celestrak.org/SOCRATES/)

### R16

Orekit developer discussion. Using NRLMSISE-00 with numerical propagator. 3 August 2022. Clarifies the SGP4 initial state and its use to initialize numerical propagation.

[Developer explanation](https://forum.orekit.org/t/using-nrlmsise-00-with-numerical-propagator/1875)

### R17

Orekit. Orbit Determination architecture. Measurement modeling, propagation, residuals, and estimation.

[Estimation architecture](https://www.orekit.org/site-orekit-11.0.1/architecture/estimation.html)

### R18

Levit, C., and Marshall, W. Improved orbit predictions using two-line elements. Advances in Space Research 47(7), 1107–1115, 2011; arXiv:1002.2277. Numerical fitting to states from successive TLEs and tested forecast improvements.

[Research paper](https://arxiv.org/abs/1002.2277)

### R19

Jah, M. K. Theory of Epistemic Abductive Geometry (TEAG): A Unified Theory of Admissibility-Driven Inference Across Dynamical Systems, Measure Theory, and Language. Preprint, version 3, 2026. Proposed inference framework.

[Versioned preprint](https://doi.org/10.20944/preprints202603.2010.v3)

### R20

Jah, M. K. The Epistemic Support-Point Filter as a Tropical Hamilton–Jacobi System: Wavefront Propagation and Possibilistic Inference. Preprint, version 3, 2026. Proposed filtering framework.

[Versioned preprint](https://doi.org/10.20944/preprints202603.2110.v3)

# Evidence-Supported ASO Catalog

An attributed orbital catalog for Space Data Network

Anthony "TJ" Koury III and Dr. Moriba Jah

Koury: Edgesource, Space Data Network. Jah: The University of Texas at Austin; GaiaVerse Ltd.

Technical whitepaper 1.8.1 | Revised 1 October 2026 (reference attribution corrected; content of 1.8, 28 September 2026)

Numerical evidence cutoff: 21 September 2026

## Executive summary

Space Data Network (SDN) is developing a reproducible, multi-provider catalog of anthropogenic space objects (ASOs) for distributed data sharing, orbit prediction, and conjunction assessment. We select solutions according to the evidence supporting each object, the intended use, and the prediction interval. Numerical high-precision orbit propagation (HPOP) is a principal computational capability; its value depends on the initial state, force model, measurement information, and treatment of uncertainty.

The catalog preserves original provider products and records how each selected or derived solution was obtained. It distinguishes direct observations from provider estimates, identifies shared source lineage, and makes model assumptions and unresolved parameters visible. Versioned dynamics and measurement models, observability reports, and explicit uncertainty representations extend the catalog's existing commitments to attribution and bounded claims.

A TLE evaluated through SGP4 at its epoch yields an estimated Cartesian state that can initialize numerical propagation after a proper frame transformation. This handoff is a legitimate computational operation. Its predictive benefit is an empirical question: errors in the estimated epoch state affect both propagation paths, while different dynamics change how those errors evolve. Neither retaining SGP4 nor switching to a numerical model guarantees the more accurate forecast. A standard TLE supplies no covariance, however, and its epoch state is a point estimate whose errors are entangled with SGP4 dynamics. A TLE-seeded numerical product therefore carries an explicit epistemic uncertainty representation for its initial condition, or is marked as having none.

The recorded Vimpel study converted all 13,808 acquired element rows and reduced withheld-position RMS discrepancies from 6.04–10.71 km to 52.82–69.93 m on three four-hour arcs. These are bounded consistency and model-reconciliation results against a provider ephemeris. Independent absolute accuracy, calibrated catalog-wide uncertainty, and operational collision probabilities remain to be established ([R1](#r1)–[R3](#r3)).

This edition retains the numerical evidence baseline of 21 September 2026. It adds a classification of inputs by data level and of TLE fields by level of measurement, a validation path for TLE-seeded numerical propagation, object-specific modeling and measurement requirements, and proposed admissible-set inference and screening. It also adds a roadmap for developing an observations-based, accuracy-assessed catalog (section 16), measured against the publicly documented U.S. Space Force capability built around the Astrodynamics Support Workstation (ASW). These additions describe requirements and research directions; they do not add completed experiments to the evidence record.

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

### Levels of data

Orbit products sit at different distances from the observations. We use four processing levels, defined here for orbit data by analogy to Earth-science data levels. Each step adds model contributors and discards information that later steps cannot recover.

| Level | Content | Examples among SDN inputs | What processing adds |
| --- | --- | --- | --- |
| 0 | Raw sensor data: detector counts, signal samples, images, time tags as recorded | Sensor output before reduction | Nothing yet; closest to contributor 1 |
| 1 | Calibrated, time-tagged measurements: angles, ranges, range rates, photometry | Reduced observations from participating sensors | A measurement model (contributor 4) |
| 2 | Estimated states and ephemerides: osculating state vectors or elements, and trajectories propagated from them | Vimpel elements and ephemerides, operator ephemerides, GNSS precise orbits, CPF predictions | A dynamics model and an inference method (contributors 2 and 5) |
| 3 | Averaged, theory-specific elements fit to Level 2 states or Level 1 data | TLEs and GP/OMM element sets from CelesTrak, Space-Track, and Space Mapper; supplemental GP; SGP4 companions produced by SDN | A second, analytic theory (SGP4) and a second fit |

Three consequences follow. First, a higher level is not a better level; it is a more processed one, and each level inherits every flaw of the levels beneath it. Second, a product that is re-epoched or re-propagated without new Level 1 data keeps its level but carries no new evidence, so update frequency is not a proxy for evidential support. Third, derived assessments such as conjunction reports inherit the limits of the lowest-quality input they depend on. Every source record therefore declares its level, and the catalog never treats a Level 2 or Level 3 product as a Level 1 observation.

A TLE is a Level 3 product. Observations are reduced to measurements, measurements are fit to estimate osculating states, and those states, or the measurements directly, are averaged into SGP4 mean elements. The TLE records none of the intervening steps: not the observations used, the fit arc, the measurement weights, or the residuals. What it encodes is SGP4's best compromise over some unstated interval, expressed in quantities that are defined only within SGP4.

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
| stateRef and validity | The selected or derived state, reference epoch, and applicable interval. |
| evidenceRefs and lineage | Observations and provider claims, their classifications, dependencies, and source editions. |
| dynamicsModelRef | Force model, environmental inputs, and parameters marked estimated, bounded, or assumed. |
| measurementModelRef | Sensor geometry, timing, calibration, bias treatment, and known or undisclosed provider modeling. |
| inferenceRef | Estimation or selection method, weights, priors, assumptions, and software version. |
| observabilityReport | Constrained and unresolved parameters and the evidence supporting that assessment. |
| uncertaintyStatus | Available covariance, admissible-set bounds or reference, calibration evidence, and limitations. |

These fields define a logical contract, not a claim of complete implementation in an SDS schema. Authority is reproducible within a declared policy: identical inputs, module artifacts, and policy should yield the same decisions within stated numerical tolerances. Hashes establish byte identity and signatures establish attribution; physical validity requires separate evidence.

The implemented Catalog Editor composes immutable source editions and preserves selected record bytes. Its matcher evaluates proposed associations without automatically publishing identity merges. End-to-end orchestration and the added evidence and uncertainty contracts remain integration work. ([R1](#r1), [R2](#r2))

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

The historical provider-fleet report records 13 public-source lanes passing signed publication, independent IPFS retrieval, pagination, and byte-hash checks: SpaceX Starlink, Eutelsat OneWeb, Planet Labs, NASA ISS, SES, Intelsat, Telesat, China Space Station, IGS/BKG GPS Precise, ESA GLONASS Precise, ESA Precise Orbit Determination, EUMETSAT, and ESA CPF Predictions. These development instances were named for their sources; they were not services operated by those organizations. ([R7](#r7))

Acquisition was incomplete in some lanes: 38 of 41 SES resources were recorded and Starlink acquisition was continuing. Authenticated Spire Global, Space-Track, EDC CPF, and Vimpel adapters were listed. Later Vimpel acquisition evidence supersedes that report's then-unverified Vimpel status without changing the status of other sources. Transport checks do not establish orbit accuracy or current service availability. ([R2](#r2), [R3](#r3), [R7](#r7))

Each edition retains format version, interval, access restrictions, retrieval result, and upstream lineage. Authorized raw measurements are a development priority because they support direct testing of object and sensor models. When only provider estimates are available, the catalog can still compare, select, and reconcile them with explicit limits. ([R8](#r8))

## 4 Normalization and epoch state physics

### Vimpel osculating elements

Vimpel's documented 15-column product includes native object number, first observation date, UTC reference epoch, update age, semimajor axis, inclination, ascending-node longitude, eccentricity, argument of latitude, argument of perigee, effective area-to-mass, magnitude, and two uncertainty indicators. Angles are in degrees, semimajor axis is in kilometres, and the stated frame is J2000. This is a provider-specific osculating product. ([R3](#r3), [R10](#r10))

Let a be the semimajor axis, e the eccentricity, i the inclination, $\Omega$ the ascending-node longitude, $\omega$ the argument of perigee, u the argument of latitude, $\nu$ the true anomaly, p the semilatus rectum, and $\mu$ Earth’s gravitational parameter. Let $\mathbf{r}_{\mathrm{pf}}$ and $\mathbf{v}_{\mathrm{pf}}$ be the position and velocity in the perifocal frame, $R_x$ and $R_z$ elementary rotations about the x and z axes, Q the rotation from perifocal to J2000 axes, and $\mathbf{r}_{\mathrm{J2000}}$ and $\mathbf{v}_{\mathrm{J2000}}$ the resulting J2000 position and velocity. At the reference epoch, true anomaly is the argument of latitude minus the argument of perigee. With consistent angles, Earth gravitational parameter $\mu$, eccentricity e, and semimajor axis a, the conversion is:

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

Here Q rotates the perifocal vectors using ascending-node longitude $\Omega$, inclination i, and argument of perigee $\omega$. The implemented reader uses $\mu$ = 398600.4418 km³/s², explicit internal SI conversion, and canonical OPM output in km and km/s. It retains `vimpel:<nativeID>` and the source descriptor without fabricating a NORAD identifier or international designator. This is an instantaneous conversion, not propagation to another epoch. ([R1](#r1)–[R3](#r3))

### Position samples and velocity

Numerical differentiation of provider positions is a diagnostic, with error that depends on spacing, rounding, stencil, and orbital geometry. At the archive's 600-second spacing, the 64-object audit found differences up to about 1.106 km/s between analytic and endpoint-derived velocities, and up to 1.824 km/s between derivative stencils. These discrepancies rule out unconditional use of the endpoint derivative, but do not determine which product is absolutely accurate. ([R3](#r3))

The preferred initial velocity is the analytic osculating velocity or a validated fitted state. Position-only evidence remains position-only; a missing velocity is not replaced by zero. Reducing a differentiation step does not necessarily improve an estimate when timestamps and positions have finite precision.

### Frames and time

Transformations identify conventions and supporting data. The recorded Vimpel path transforms J2000 to GCRF for HPOP and back for comparison, and UTC sample times to TDB for propagation. TEME, terrestrial frames, J2000 conventions, and GCRF are distinguished explicitly. Earth-orientation, leap-second, and ephemeris inputs must be versioned where required. ([R1](#r1), [R11](#r11))

## 5 Initializing numerical propagation from a TLE

A TLE is a set of model-specific mean elements. Evaluating it with SGP4 at its reference epoch produces SGP4's estimated instantaneous Cartesian position and velocity. After a consistent frame transformation, that state can serve as the initial condition for numerical integration. We distinguish the validity of this initialization from the empirical accuracy of the resulting forecast. ([R13](#r13), [R16](#r16))

### The handoff contract

First, preserve the TLE bytes, epoch, identifiers, source edition, and SGP4 implementation and constants. Evaluate SGP4 at zero elapsed time, including the appropriate near-Earth or deep-space behavior. Use its returned position and velocity; do not treat the mean elements as ordinary osculating Keplerian elements.

Second, transform the complete state from TEME to the numerical integration frame at the same physical instant. The velocity transformation must include the frame's time dependence where required. Record the transformation conventions, supporting data, units, and time scales. Verify a round trip and confirm that the transformed state and numerical initial state agree within declared tolerances.

Third, declare the numerical force model, environmental inputs, integration settings, and object parameters. A TLE alone does not supply independently determined mass, attitude, drag coefficient, or radiation-pressure coefficient. Its $B^*$ parameter belongs to the SGP4 drag model; any mapping into a numerical drag model requires explicit assumptions and validation.

Finally, publish the resulting trajectory as a derived, TLE-seeded numerical product with its parent state, model lineage, and initial-condition uncertainty status. Retain the native SGP4 product and identify the interval over which the numerical product has been assessed.

### What the handoff establishes

The two paths start from the same estimated position and velocity, expressed in a common frame. They subsequently evolve according to different dynamics. Applying numerical force terms after the handoff does not by itself double-count the perturbations used to obtain the epoch state: those terms govern subsequent evolution. Their adequacy and parameter values still require assessment.

A state error at epoch is an error for both paths. Continuing with SGP4 does not remove it, and switching models does not create it. Different models can nevertheless amplify, reduce, or partly compensate its effect on predicted positions. Neither model complexity nor model consistency alone decides which forecast is closer to the object.

The reported Vimpel study used osculating elements and provider ephemerides; it did not test this TLE handoff. No predictive advantage for TLE-seeded numerical propagation is claimed from that experiment.

### Levels of measurement of TLE elements

The fields of a TLE are not measured on a common scale. We classify each by level of measurement: nominal (labels, supporting only equality), ordinal (ordering only), interval (meaningful differences, arbitrary zero), and ratio (meaningful differences and ratios, true zero). Angles that wrap around a circle form a further directional, or circular, scale on which ordinary arithmetic fails at the wrap point.

| TLE field | Level of measurement | Implications for computation |
| --- | --- | --- |
| Catalog number | Nominal | Identifier only; equality tests, no arithmetic or ordering. Joined only with its namespace. |
| Classification | Nominal | Category label; no arithmetic. |
| International designator | Nominal | Composite identifier encoding launch year, launch number, and piece; its parts are labels, not quantities. |
| Epoch (year, fractional day) | Interval | Differences are meaningful and define propagation age; ratios are not. The two-digit year needs an explicit century rule (conventionally 57–99 map to 1957–1999 and 00–56 to 2000–2056). |
| First derivative of mean motion / 2 | Ratio (signed) | Not used by SGP4; carries no information into SGP4 propagation and should not be treated as a constraint. |
| Second derivative of mean motion / 6 | Ratio (signed) | Not used by SGP4; same caution as above. |
| $B^*$ | Ratio in form (signed) | A fitted SGP4 parameter, in inverse Earth radii, that absorbs unmodeled forces and can be negative. Arithmetic is defined, but it is not a physical drag coefficient and does not map directly to one. |
| Ephemeris type | Nominal | Label; normally zero in distributed sets. |
| Element set number | Ordinal | Supports ordering only. Increments do not count new fits and may skip or repeat. |
| Checksum | Nominal | Integrity check for the line; not data. |
| Inclination | Ratio on a bounded range (0 to 180 degrees) | Typically the best-determined element. Not circular, but the geometry becomes singular near 0 and 180 degrees, where the node is undefined. |
| Right ascension of ascending node | Directional (circular) | Differences must be wrapped; arithmetic means are invalid near the wrap point; circular statistics apply. Ill-defined for near-equatorial orbits. |
| Eccentricity | Ratio on a bounded range (0 to 1) | True zero and meaningful ratios, but bounded; near zero the argument of perigee loses meaning. |
| Argument of perigee | Directional (circular) | Wrapped differences and circular statistics; ill-defined for near-circular orbits, where it trades off against mean anomaly. |
| Mean anomaly | Directional (circular) | Encodes along-track phase, usually the least-determined quantity and the fastest-growing error. Circular treatment required. |
| Mean motion | Ratio | Tied to orbital energy and typically well determined. The TLE carries a Kozai mean motion, which SGP4 converts internally to a Brouwer mean motion, so it is not interchangeable with an osculating value or with mean motion from another theory. |
| Revolution number at epoch | Ratio (count), stored modulo 100,000 | Wraps in the fixed-width format and is often unreliable; not suitable as a constraint. |

These differences have direct mathematical consequences:

1. The element set is not a vector space. Mixed scales and circular components mean that vector addition, arithmetic means, Euclidean distances, and ordinary covariance matrices over raw TLE fields are not well defined. Circular elements need wrapped differences and circular statistics, and near-circular or near-equatorial orbits require nonsingular element combinations or a Cartesian state representation before any averaging or differencing.

2. Nominal and ordinal fields support identity and sequencing, not computation. In particular, a change in element set number or epoch does not show that new observations were fit.

3. Determinability differs sharply across elements. Inclination and mean motion, and hence orbital energy, are usually well constrained, while along-track phase is weakly constrained and dominates forecast error as it grows with propagation age. Weighting all elements equally, or assigning them a common isotropic uncertainty, misrepresents what a TLE supports.

4. The fixed-width format quantizes every value. Let a be the semimajor axis and $\Delta M$ the mean anomaly resolution in radians; the along-track position resolution is then approximately a × $\Delta M$. With $\Delta M$ equal to 0.0001 degree and a near 6,900 km, this is about 12 m per encoding increment for a near-circular orbit (about ±6 m for this component under round-to-nearest). This is encoding resolution, not a bound on total physical position error. Quantization is a hard, bounded error on what a TLE can represent and belongs in its uncertainty representation as a bounded term rather than a Gaussian one. Inclination, ascending-node longitude, and argument of perigee are encoded to the same 0.0001 degree, so each carries a comparable term of roughly 12 m at this radius, cross-track for inclination and node (the node term scaled by the sine of inclination) and in-plane for argument of perigee. Eccentricity is encoded to $10^{-7}$, contributing about 0.7 m in radial extent. Mean motion is encoded to $10^{-8}$ revolutions per day, which produces an along-track drift of about 0.43 m per day per encoding increment at this radius (about 0.22 m per day for half-increment rounding), negligible beside other TLE error sources.

5. TLE elements are theory-specific. Mean elements share names with osculating elements but denote different quantities, so a TLE cannot be compared field by field with Vimpel's osculating elements or any Level 2 product. Comparison happens only after evaluating SGP4 and transforming to a common state and frame.

6. Some fields carry no information into propagation. The mean-motion derivatives are ignored by SGP4, and $B^*$ is a fitted compensator rather than a physical parameter. Any uncertainty method should say which fields it treats as evidence.

### Carrying TLE uncertainty through the handoff

Because a TLE is a Level 3 product, its errors are dominated by epistemic limits, such as an unknown fit arc, unknown observation geometry, and unmodeled forces absorbed into $B^*$, rather than by random noise, and a standard TLE supplies no covariance. The SGP4 epoch state inherits those limits. Its errors are correlated with SGP4's own dynamics, because the fit that produced the elements allowed initial-state and model errors to compensate (section 6). A numerical integrator does not share that compensation, so the point state alone understates what is unknown at the moment of handoff.

We therefore require that a TLE-seeded numerical product carry an initial-condition uncertainty representation derived from the TLE, available supporting evidence, and explicitly declared assumptions: a set of epoch states, or an equivalent bounded region, that those inputs cannot rule out under those assumptions, together with a process-uncertainty allowance for the difference between SGP4 dynamics and the object's actual motion. The construction should respect the measurement scale and determinability of each element described above, include the format's quantization as a bounded error, and support updating one TLE-derived region with a later TLE while recognizing that the later element set may not reflect new Level 1 evidence. Because the element set is not a vector space, admissible-region methods of the kind described in section 9 are a natural fit.

Methods of this kind based on TEAG and the ESPF are in development and are not part of the present evidence baseline. Until such a representation is validated, a TLE-seeded numerical product is published with its initial-condition uncertainty marked unknown, and its forecast is assessed only through the controlled comparison in section 6.

## 6 Estimation and model transfer validation

### What an orbit fit optimizes

Orbit determination estimates parameters by comparing predicted measurements with observations over a fitting arc. In weighted least squares, the objective is a weighted residual sum, potentially with priors or regularization. It does not generally minimize the physical position and velocity error specifically at the solution epoch. Sensor geometry, measurement errors, force-model errors, and fitted parameters all influence the inferred epoch state. ([R17](#r17))

Initial-state and dynamics errors can compensate within a fit. A velocity that is too high can partly compensate for an acceleration that is too low over the observation arc. Both remain errors in the original model. Replacing the acceleration model while keeping the estimated velocity changes that compensation; it can improve or degrade the forecast. Model dependence therefore motivates testing rather than a presumption that SGP4 or numerical propagation is superior. ([R13](#r13))

Formal covariance describes uncertainty under the estimator's assumed models and error statistics. For a linear model with correctly specified dynamics and measurement models, zero-mean errors, and weights equal to the inverse measurement-error covariance, weighted least squares yields the minimum-variance linear unbiased estimate, and its formal covariance describes the actual error. When those conditions fail, as they do whenever model error is present, the formal covariance no longer measures actual error, and a small formal covariance can coexist with systematic bias. A standard TLE does not itself provide a full state covariance. The catalog records what uncertainty was supplied, inferred, calibrated, or remains unknown.

### A controlled comparison

For each test object and TLE edition, evaluate native SGP4 and numerical propagation initialized from its epoch state over the same forecast interval. Use common frame and time conventions and an independent reference with documented accuracy, lineage, and maneuver treatment. Report reference uncertainty along with position and velocity errors, radial/along-track/cross-track components, maxima, percentiles, and error growth with prediction age. Where an initial-condition uncertainty representation is available, also report how often the independent reference falls within the propagated region, so coverage is tested rather than assumed.

Stratify by orbit regime, perigee altitude, eccentricity, source age, physical-parameter knowledge, maneuver status, and forecast length. Use the same test cases and include failures. Distinguish tests supplied with additional physical information from tests using only TLE information. Freeze models and tuning before evaluating an untouched forecast set.

### Optional fitting across an arc

A numerical initial state and identifiable parameters may be fitted to an SGP4-generated arc or a history of TLE-derived states. This can improve consistency across the transition. Research has demonstrated improved forecasts from fits to successive TLE-derived states in tested cases, but the result is not universal. Such fitting remains inference from provider estimates and may inherit their biases or correlations. ([R18](#r18))

An arc fit is an optional model-transfer technique, not a prerequisite for initializing an integrator. When independent observations are available, direct estimation under the chosen numerical and measurement models offers a stronger basis for evaluating physical accuracy. The acceptance criterion is performance for the intended use, with supported uncertainty and transparent failures.

## 7 Object association and solution selection

### Candidate generation

Candidate associations combine provider crosswalks, international designators, native-identifier histories, and coarse orbital compatibility. Vimpel's datefirst fields Nvym, t_det_v, Nnor, and t_det_n supply attributed identity evidence. Preserve dates, leading-zero normalization, and contradictory or duplicate declarations. Names and unqualified numeric identifiers are not sufficient join keys. ([R3](#r3))

Similar trajectories can describe formation members, recent deployments, or fragments. Pairwise compatibility is not automatically transitive. Proposed identity groups need component-wide conflict checks and a consistent one-to-one assignment where the catalog semantics require it. Unresolved records remain separate, and observations that fit no existing candidate retain an unassigned or new-object hypothesis.

### Physical compatibility

Compare trajectories on a common origin, frame, time scale, start epoch, and sampling grid over overlapping validity intervals. The implemented matcher accepts already normalized OEM inputs; the caller performs the required propagation and transformations. It assesses position, velocity, and derivative consistency against explicit thresholds. Its verdict is trajectory compatibility rather than automatic identity publication. ([R2](#r2))

Report RMS and maximum discrepancies, duration, sample count, source epochs, and propagation ages. Require sufficient coverage rather than one coincident position. Treat maneuvers, discontinuities, and non-overlapping arcs explicitly. Thresholds belong to the versioned recipe and require calibration; future uncertainty-aware tests should retain their assumptions about correlation and admissible sets.

### Selecting a solution

For an accepted identity, exclude invalid or inapplicable products, then evaluate validity coverage, independent support, measurement and dynamics quality, maneuvers, uncertainty calibration, predictive performance, propagation age, source lineage, and declared source preference. Selection produces a source reference and an explanation appropriate to the requested use.

A numerical solution can be preferred when evidence supports its forecast, including a validated TLE-seeded solution. A native SGP4 or operator solution can be preferred when it is better supported. Freshness and propagator sophistication alone are insufficient ranking criteria. Position, velocity, and uncertainty must form a consistent solution; they are not spliced from unrelated estimates.

Review outcomes are compatible, rejected, ambiguous, or insufficient. Accepted links retain the reviewer or decision policy and evidence so later information can reverse an association without rewriting history. The current recipe-v2 editor uses international designators for authoritative composition. Objects without accepted designators remain review candidates even where the current CAT export excludes them. ([R2](#r2))

## 8 Dynamics and measurement models

### Shared physics and object parameters

A dynamics model combines well-characterized shared physics, object-specific effects, and residual model error. Let r be the object position with time derivatives $\dot{\mathbf{r}}$ and $\ddot{\mathbf{r}}$, t time, $\mathbf{a}_{\mathrm{gen}}$ the acceleration from shared physics, $\mathbf{a}_{\mathrm{obj}}$ the object-specific acceleration depending on a parameter vector p, and $\delta(t)$ the residual model discrepancy. A useful decomposition is:

$$
\ddot{\mathbf{r}} = \mathbf{a}_{\mathrm{gen}}(\mathbf{r},\dot{\mathbf{r}},t) + \mathbf{a}_{\mathrm{obj}}(\mathbf{r},\dot{\mathbf{r}},t;\mathbf{p}) + \boldsymbol{\delta}(t)
$$

The shared term includes the selected gravity, third-body, tidal, and relativistic models. The object term depends on parameters such as area-to-mass, drag and radiation response, attitude, and thrust. The residual term represents remaining model discrepancy, with declared bounds or an appropriate stochastic description.

Known force terms need not be estimated anew from every object's observations to be useful. Observability limits the estimation of unknown parameters and the confidence assigned to them; it does not prohibit using independently supported physics. An unresolved parameter may be bounded, marginalized, or assigned a documented prior. The choice of model complexity considers sensitivity, available evidence, and the forecast interval.

The dominant errors depend on orbit, altitude, object behavior, measurement quality, and prediction age. High eccentricity alone does not establish strong atmospheric drag; perigee altitude and atmospheric conditions also matter. Track parameter histories and model residuals across arcs. Suspected maneuvers trigger a validity review and, where appropriate, a new arc or a model with explicit maneuver uncertainty.

### Measurement models as versioned artifacts

A measurement model maps the object and sensor states to a predicted observable. It includes geometry, timing, calibration, frame conventions, and relevant corrections such as light-time, aberration, and atmospheric refraction. Let y be the observable, x the object state, p its physical parameters, s the sensor state and calibration, t time, h the measurement model, b the modeled bias, e the random error, and $\varepsilon$ the residual measurement-model discrepancy. Bias, random error, and residual measurement-model discrepancy are represented distinctly:

$$
\mathbf{y} = h(\mathbf{x},\mathbf{p},\mathbf{s},t) + \mathbf{b} + \mathbf{e} + \boldsymbol{\varepsilon}
$$

A missing or undisclosed provider measurement model is recorded as unknown.

Each contributing sensor should publish a versioned model with location, timing provenance, pointing and calibration history, error characterization, and known limitations. At an illustrative speed of 7.5 km/s, a 1 ms timing offset corresponds to 7.5 m of travel; its measurement impact depends on geometry. Photometric inference states its reflectance and attitude assumptions.

Estimate biases when the observations can constrain them, otherwise carry defensible bounds or priors. Independent reference trajectories and observations across multiple objects can help separate sensor errors from orbital model errors. Residuals alone generally do not identify which contributor caused a discrepancy.

## 9 Inference and supported uncertainty

The catalog supports estimation methods whose assumptions and uncertainty can be examined. Least squares, probabilistic filters, and bounded-set methods are evaluated according to their model treatment, calibration, computational cost, and performance. Least squares can include estimated biases, physical parameters, and model-error terms; a state-only fit is one particular configuration.

### Covariance and model discrepancy

Preserve provider uncertainty statements with their stated semantics. Vimpel's two 50%-confidence indicators do not define a full six-dimensional covariance, correlations, a Gaussian distribution, or hard bounds. A fitting regularizer and small residuals likewise do not supply a validated physical covariance. ([R3](#r3), [R10](#r10))

A covariance product must specify its state, epoch, frame, distributional assumptions, measurement errors, dynamics, model discrepancy, and calibration evidence. Propagation may use a state-transition model and a declared process-noise contribution, but unvalidated tuning is not a substitute for independent coverage tests. Unknown cross-provider correlations preclude treating simple inverse-covariance averaging as automatically justified.

### Admissible trajectories

An admissible set contains states, parameters, and associated trajectories consistent with the evidence under explicitly declared assumptions and error allowances. Parameter ranges, measurement-error sets, and dynamics-discrepancy bounds define what can be excluded. Provider claims require their own uncertainty treatment and dependency information before acting as constraints.

Bounds are substantive modeling choices. When errors have unbounded tails, a finite envelope must state its coverage or truncation convention rather than claiming an absolute guarantee. New evidence can contract a fixed hypothesis set; propagation with process uncertainty can expand it. Maneuvers, revised bounds, or inadequate models can require reopening or expanding the hypotheses.

### Proposed TEAG and ESPF evaluation

We propose evaluating the Theory of Epistemic Abductive Geometry (TEAG) and the Epistemic Support-Point Filter (ESPF) as an implementation path for inference using admissible sets. Their support-point and bounding-geometry approach expresses plausibility through possibility and necessity and accommodates unresolved alternatives. Possibility describes compatibility with evidence; necessity describes support through exclusion of alternatives. These quantities are not collision probabilities. ([R19](#r19), [R20](#r20))

The SDN evaluation must document bound construction, support resolution, treatment of outliers and inconsistent evidence, and behavior under model mismatch. It must compare against appropriate probabilistic and bounded-set baselines using independent cases. Published bounds must state whether they conservatively enclose the admissible trajectories or only summarize sampled support. TEAG/ESPF integration and catalog-scale validation are proposed work, not results of the Vimpel experiment.

Disclosure: TEAG and the ESPF ([R19](#r19), [R20](#r20)) were developed by co-author M. K. Jah, who is also a co-founder of GaiaVerse Ltd., which develops implementations of these methods. Baselines and independent test cases for the SDN evaluation should therefore be selected and scored independently of the authors' own implementations.

## 10 The measured Vimpel refinement study

### Method and experimental configuration

The implemented refinement estimates a six-component initial-state correction using finite-difference sensitivities from one nominal and six positively perturbed trajectories. It checks initial-state bindings and requires six numerically independent sensitivity columns. These derivatives measure sensitivity to the initial state; they do not differentiate coarse provider positions to obtain velocity. ([R1](#r1))

A regularized weighted least-squares update uses training positions. Corrections outside configured position and velocity scales are rejected. A candidate has stale Keplerian fields cleared and is returned as candidate-needs-propagation. Repropagation and validation are mandatory, with an optional prior withheld-RMS gate rejecting deterioration. The module does not emit a provider covariance or automatically promote a catalog solution. ([R1](#r1))

Each of three four-hour arcs contained 25 positions at 600-second spacing. Withheld indices 3, 7, 11, 15, 19, and 23 left 19 training and six validation positions. One update was performed. The force model used central Earth gravity, J2–J4, analytical Sun/Moon perturbations, and RKF78 integration; drag and solar-radiation pressure were disabled. ([R1](#r1))

| Eccentricity | Initial withheld RMS (km) | Fitted withheld RMS (m) | Maximum withheld residual (m) |
| --- | --- | --- | --- |
| 0.004351 | 10.70686 | 69.929 | 97.349 |
| 0.892913 | 6.04357 | 59.097 | 72.843 |
| 0.840641 | 10.41481 | 52.821 | 89.683 |

### Interpretation and next measurements

Both training and withheld maxima passed the example 1 km gate, which was experimental tuning rather than an operational safety threshold. Withheld samples were excluded from estimation but came from the same provider product. The results demonstrate software capability and short-arc consistency between models; they do not establish independent absolute accuracy or a representative catalog-wide error distribution.

Vimpel's published model includes degree-8 Earth gravity, DE405 Sun/Moon, atmosphere, and radiation pressure. The tested configuration differs and the current GOST selector is a placeholder. The initial 6–11 km discrepancies merit decomposition, but the study does not identify their individual causes. A state correction can absorb some model mismatch over four hours. ([R1](#r1), [R3](#r3), [R10](#r10))

The next study should vary force terms and conventions systematically, report state corrections and sensitivity conditioning, assess longer predictions across the available ephemeris, and compare with independent reference products. Include failed cases and supported uncertainty envelopes. The original numerical results and their bounded interpretation remain unchanged.

## 11 Chinese AOE catalog integration

### Source and model identification

This design interprets the proposed Chinese catalog as Space Mapper's AOE catalog, subject to confirmation of the intended provider. Its documented families include self-determined AOE orbits, international multi-source orbits, and a combined product. The self-determined family's use of its parent company's observation network is a provider assertion to preserve and assess. ([R4](#r4), [R5](#r5))

The documented orbit-list API uses bearer-token authentication and KY and INTERNATIONAL channels, with TLE, two-line TLE, JSON, and OMM-XML responses. Its OMM example declares Earth, TEME, UTC, and SGP4. A JSON example uses a timezone offset. The catalog API exposes MIXED, AOE, and INTL sources and distinguishes catalog identifiers from NORAD identifiers. Actual acquired responses must be checked against these examples. ([R6](#r6), [R9](#r9))

### Adapter and provenance requirements

Maintain separate namespaces such as spacemapper:KY and spacemapper:INTERNATIONAL. Retain the requested channel, response metadata, native catalog identifier, asserted international links, epoch string, format, exact bytes, and upstream lineage. Normalize timezone-qualified epochs while preserving their original representation. Numeric identifiers are joined only with their namespaces and supporting association evidence.

An SGP4 mean-element product is evaluated through SGP4 before any numerical initialization or fit. It does not pass through the Vimpel osculating-element converter. Missing frame, time, or model metadata yields a validation failure or explicitly incomplete product. The TLE handoff and validation requirements in sections 5 and 6 apply to derived numerical solutions.

AOE, international, and mixed channels may share upstream information. Preserve a lineage graph and avoid counting duplicated inputs as independent evidence. A demonstrably independent KY estimate could be especially useful for testing agreement and disagreement across sources. Its value still depends on quality, geometry, uncertainty, and relevance to the intended interval; independence alone does not guarantee accuracy.

### Admission study

Before acquisition and combination, document applicable authorization, licensing, and any required export-control review. Acquire an authorized immutable snapshot, verify pagination and completeness, record counts and hashes, and test representative formats and orbital regimes. Compare time-aligned products and report coverage, consistency, conflicts, unknown lineage, and unavailable cases separately.

No acquired AOE coverage count, completed integration, improved conjunction accuracy, or legal conclusion is asserted here. The historically tested China Space Station operator feed is a separate source and provides no evidence of AOE catalog ingestion.

## 12 Conjunction assessment and uncertainty

### Two validation baselines

The first baseline is a controlled SGP4 replay against SOCRATES using identical input editions where available, the same assessment interval, compatible model conventions, and explicit screening settings. CelesTrak identifies SGP4 and STK/CAT in its methodology. The designated acquisition path remains the celestrak.eth test node. Preserve catalog and report hashes and record unavailable proprietary settings. A live report compared with a later catalog is not a controlled replay. ([R15](#r15))

The second baseline assesses the selected catalog, including numerical and other supported solutions, against independent orbit evidence and conjunction cases. Report closest-approach time and miss-distance errors, missed and additional encounters, coverage, failures, screening bounds, and uncertainty assumptions. A catalog using different orbital evidence or dynamics need not reproduce every GP-derived SOCRATES event.

### Screening admissible trajectories

For two objects, propagate their admissible trajectory sets over a common interval. An encounter is geometrically possible under the declared bounds if some jointly admissible pair comes within the combined hard-body radius at the same time. Where the objects share errors or evidence, joint admissibility must account for those dependencies rather than automatically taking every combination of marginal states.

A conservative enclosing bound can support exclusion when it remains separated from the collision region. Intersection of conservative envelopes may be only a candidate requiring refinement, since an envelope can contain states that no admissible trajectory reaches. Finite support-point samples alone cannot guarantee that all encounters have been screened; coverage and enclosure assumptions must be explicit.

Possibility and necessity scores require a specified possibility model and event definition. A geometric intersection by itself supplies no calibrated probability or graded score. Let A be an event, Aᶜ its complement, Π(·) the possibility measure, and N(·) the necessity measure. In a normalized possibility model, $N(A)=1-\Pi(A^{c})$: an event is necessary only to the degree that its complement is implausible. For the event "no collision," a high necessity therefore requires that every collision hypothesis be of low possibility, while a low necessity means only that collision has not been excluded. Report the definitions used. A low necessity of collision is not evidence of safety; a safety claim rests on a high necessity of no collision, which in turn requires the enclosure assumptions above. ([R19](#r19), [R20](#r20))

### Operator reporting and tasking

When supported, report ranges of closest-approach times and miss distances and identify whether initial-state uncertainty, unresolved object parameters, dynamics error, or measurement error dominates the encounter. Those findings should identify observations that could resolve an ambiguity. Wider uncertainty can increase screening workload; acceptance testing must measure missed encounters, false alerts, and computational cost.

Collision probability remains available when the relative-state uncertainty model has suitable independent calibration and states its cross-correlation, geometry, and hard-body-radius assumptions. No live SOCRATES parity, validated admissible-set screening, or collision-probability result is claimed by the present evidence baseline.

## 13 Publication and the operator experience

### Immutable distribution

Publication proceeds transactionally: acquire and preserve bytes, verify integrity, normalize and validate, retain permitted source and derived products, publish a signed manifest, then announce the new catalog head. A conjunction worker consumes a complete immutable snapshot. Update events identify the object, prior and new solution references, effective epoch, recipe, and provenance. Retries are idempotent.

Content addressing, independent replication, and open computation support resilience. They do not guarantee availability or override provider restrictions. Pin receipts identify retained bytes and actual nodes. A public notice may reference a restricted product without publishing its contents or credentials. Module availability, successful invocation, and validated output publication are separate operational checks.

An intended conjunction service node is one placement of an auditable service that other authorized nodes can reproduce. Its availability, placement, throughput, and restart behavior require live verification. The recorded offline study does not establish operational readiness.

### Feedback and reversible decisions

New observations append evidence and trigger reevaluation of affected identities, solutions, and conjunctions. Corrections retain the provider originals and indicate which derived results are superseded. A feedback record identifies the disputed solution, supporting evidence, proposed correction, module and policy versions, and disposition. It distinguishes malformed inputs, stale data, maneuvers, incompatible models, and unresolved identity.

Model revisions and changed error bounds are versioned events. A trajectory excluded under one model may become admissible under a corrected model; history must preserve why the earlier decision was made. New-object and unassigned hypotheses remain available where current explanations are inadequate.

### Catalog and Space Aware interface

The proposed workspace presents ordered source layers, coverage, freshness, integrity, and a paginated object table. An object comparison exposes native and derived solutions, evidence classifications, lineage, model versions, frame and time conventions, residuals, uncertainty status, and selection reasons. Observability gaps and admissible-set limits should be visible alongside a representative trajectory.

A review action explains the ambiguity and the effects of accepting an identity link or solution. Every calculation links to immutable inputs and a recipe. The intended free orbital console exposes catalog browsing, propagation, conjunction assessment, and visualization through the same module artifacts. Help distinguishes native products, TLE-seeded numerical products, fitted solutions, consistency validation, and independent accuracy validation. These remain interface and packaging requirements rather than release claims.

### SGP4 companion products

An accepted numerical solution may be sampled and fitted with SGP4 mean elements over a declared interval, with separate fit and holdout checks before an OMM is emitted. Approximation errors and failures remain visible. An SGP4 companion for every object is a product objective, not a guaranteed useful approximation. SDS representations retain source OPM, OEM, and OMM semantics without claiming to be the CCSDS XML wire format. ([R12](#r12)–[R14](#r14))

## 14 Acceptance gates and study design

| Gate | Required evidence | Recorded status |
| --- | --- | --- |
| Acquisition | Exact bytes, source edition, integrity, permitted retention, and lineage. | Vimpel snapshots and historical fleet transport checks. |
| Normalization | Preserved identity, epoch, units, frame, time scale, and model semantics. | 13,808 Vimpel rows converted. |
| Evidence classification | Observations and provider claims tagged with dependencies and unknown lineage. Data level declared per source and measurement scale per field. | Expanded contract proposed. |
| Model reconciliation | Training and withheld residuals, state shifts, sensitivity checks, and failure counts. | Three four-hour cases; decomposition pending. |
| TLE model transfer | Verified epoch handoff and comparative forecasts against independent references. Initial-condition uncertainty carried through the handoff, with coverage tested. | Not established by the Vimpel study. |
| Object and sensor models | Versioned models, parameter observability, timing and bias treatment, and declared assumptions. | Expanded requirements proposed. |
| Physical accuracy | Independent reference comparisons stratified by regime and prediction interval. | Not established. |
| Uncertainty | Calibration of covariance or coverage and conservatism of declared trajectory bounds. | Not established. |
| Identity | Candidate review, conflict checks, reversible accepted links, and unassigned cases. | Incomplete. |
| Network flow | Update, pin, stream, restart/replay, and independent retrieval of complete snapshots. | Continuous operation not established by offline study. |
| Conjunction | Controlled SOCRATES replay and independent assessment of selected trajectories and uncertainty. | Pending. |

The next study stratifies objects by orbit regime, perigee altitude, eccentricity, source age, arc duration, observation availability, maneuver status, parameter observability, and independent-reference availability. Report coverage and failure rates alongside residual percentiles, and retain parser failures and unmatchable objects in the denominator.

Reserve a final untouched validation set. Repeated tuning against withheld points turns those points into development data. Assess the benefits of additional observations and physical information separately from benefits due only to a change in propagation model. Results should support a declared use and interval rather than a universal ranking of propagators.

## 15 Reproducibility and evidence baseline

The numerical baseline is modules commit 49e159d7003cac3d3e65b03170f11909fa86dd2c, including files/orbit-products 0.1.1 and analysis/catalog-composer 0.1.8. The recorded package suites passed 68 tests. One opt-in parity test ran separately; one full-DE440s external-fixture test was skipped. Nine explicit parity cases exercised Chromium, native WasmEdge, and the SDK container with 45 comparisons. Required repository gates passed; an advisory repository-wide gate was blocked under machine overload. ([R1](#r1))

These checks establish selected software behavior across runtimes. They do not validate all inputs, establish independent orbit accuracy, or demonstrate operational readiness. The implementation statuses and measured results in this edition refer to that baseline; the revised architecture does not imply that the new requirements have been implemented.

### Immutable evidence identifiers

Acquired Vimpel element table

```text
840cda6d499028c17a7e22228b1b22fe500078e996113ef5804c4fff7ba612fa
```

Acquired Vimpel ephemeris archive

```text
f46df9c310a8c33752d5baebf429a0583e7896f6714501b77d8fcc033c65e6bb
```

Normalizer WASM

```text
d9f8ea296eab142767a02eddcdc888899fdd8fdccd968a1b82479ff673698e82
```

Catalog WASM

```text
7d87d552215685ee6a069f42f83d411af244e69d3cab7b4880f2e378cae30eed
```

HPOP WASM

```text
7305c5ef6db04cfb5babc4f2c5f87b17b1e5901e4c14bd25cf9ca7b14b7b48b1
```

### Reproduction contract

Use the recorded commit, documented dependencies, exact artifact hashes, and authorized source files. The driver is analysis/catalog-composer/tests/vimpel-live.mjs with ELEMENTS_FILE and EPHEMERIS_RAR arguments. It routes existing C++/WASM reader, frame, time, propagation, and fitting modules. It does not acquire provider data, install modules, publish records, or create covariance. Per-case reference hashes and controls remain in the verification record. ([R1](#r1))

Reproduction of the new TLE-transfer and uncertainty studies additionally requires frozen test editions, independent-reference lineage, force and measurement models, parameter assumptions, training and validation partitions, and prediction intervals. Report unavailable inputs and rejected cases rather than silently substituting a model or source.

## 16 Closing the gap

The preceding sections position the catalog as an evidence-classified assembly of provider claims. The comparison baseline for this section is the U.S. Space Force astrodynamics capability built around the Astrodynamics Support Workstation (ASW), including the Special Perturbations (SP) orbit determination and propagation that maintain the High Accuracy Catalog (HAC). Public descriptions of that baseline include an eighth-order Gauss–Jackson integrator, the EGM96 geopotential truncated to medium degree and order with Earth and ocean tides, and the High Accuracy Satellite Drag Model (HASDM), whose published development used dynamic corrections to a Jacchia 70 density model and tracking of about 75 calibration objects ([R29](#r29), [R30](#r30), [R32](#r32)). Public research also describes HASDM with JB2008 as the background density model ([R33](#r33)). CODAC, SuperCODAC, SCCAT, and SuperCOMBO are U.S. Space Force applications. CAWR is the conjunction-analysis report product associated with SuperCOMBO. Their names provide context for the discussion of catalog accuracy; this paper does not describe their internal implementations or assert their current operational configuration. This section identifies the technical work needed for SDN to produce an observations-based, accuracy-assessed catalog. These are requirements and design proposals, not completed experiments. Matching this baseline is a reproducibility target, not an accuracy target. An open implementation that reproduced SP dynamics exactly would also reproduce SP model error for every object whose actual environment departs from those models, and the accuracy of the HAC rests on the observations, calibration objects, and operational processes behind it, which dynamics software alone cannot supply.

The catalog's own epistemics apply first. An external orbit solution remains a provider claim with its own fit arcs, weights, models, and lineage. Agreement with an authorized reference product is a validation target, not a new ground truth. Each proposed capability below belongs in the section 2 record contracts and must earn its own independent validation.

### 16.1 Capability gaps

An observations-based catalog requires uncertainty handling, observation-based correction, association, initial orbit determination, maneuver handling, accuracy assessment, configurable perturbation models, and conjunction products. The table maps those capability categories to the recorded SDN baseline and proposed work; it does not assert operational parity with another catalog.

| Gap | Capability | Recorded SDN baseline | Closing work |
| --- | --- | --- | --- |
| G1 | Uncertainty estimation and transport | State refinement without a calibrated covariance product | Establish initial uncertainty; propagate and calibrate covariance or admissible sets; serialize assumptions |
| G2 | Observation-based differential correction | Fit to provider positions with withheld-RMS checks | Add observation models, weighted batch correction, parameter treatment, and acceptance tests |
| G3 | Observation-to-track association | Identity crosswalks and trajectory compatibility | Add observation-space gates, calibrated prediction-error models, and lifecycle decisions |
| G4 | Initial orbit determination | Entry from provider states | Add angles-only IOD with suitable angular data and geometry-dependent alternatives |
| G5 | Maneuver handling | Suspected maneuvers trigger validity review | Add residual change detection and explicit maneuver hypotheses with uncertainty |
| G6 | Accuracy assessment | Independent physical accuracy not established | Publish held-out residuals and reference errors by object, regime, and prediction age |
| G7 | Force-model depth | Study used J2–J4 and analytical Sun/Moon; drag and radiation pressure disabled | Implement the closed-to-open force mapping and staged validation in section 16.7 |
| G8 | Conjunction products | Controlled replay and independent validation pending | Add CDM output with documented uncertainty and encounter validation |
| G9 | Frames and element semantics | Epoch handoff and convention requirements specified | Enforce model dispatch and add reproducible convention-comparison tests |

### 16.2 Uncertainty estimation and propagation

The covariance-based implementation path has three distinct tasks: establish initial covariance, propagate it, and test its calibration. A state-transition matrix (STM) addresses the second task. The recorded refinement already forms finite-difference sensitivities from one nominal and six perturbed trajectories. Retaining the full final state and dividing its differences by the corresponding initial perturbations provides an STM approximation, subject to perturbation-size and numerical-convergence checks. Variational integration is a durable alternative that makes sensitivities available throughout the arc.

Let t₀ be the initial epoch and t the output time, P₀ the initial-state covariance at t₀, P(t) the propagated covariance at t, Φ(t, t₀) the STM mapping state deviations from t₀ to t, and Q(t) the accumulated process covariance expressed at t. For a linearized six-state model with additive, uncorrelated process uncertainty, covariance propagation takes the form:

$$
P(t)=\Phi(t,t_0)P_0\Phi(t,t_0)^{\mathsf{T}}+Q(t).
$$

The STM does not determine $P_0$ or $Q(t)$. Initial uncertainty must come from an estimator with declared measurement-error assumptions, a documented provider product, or an independently calibrated empirical model. Uncertain physical parameters and their correlations require augmented sensitivities or an explicit consider-parameter treatment. ([R17](#r17), [R22](#r22))

Estimated parameters are solved for using the observations. Consider parameters remain unestimated while their uncertainty is accounted for. Fixed values whose uncertainty is omitted constitute a third, distinct choice. The record must identify which treatment applies; a configured uncertainty allowance does not by itself demonstrate parameter estimation.

Publish covariance at the epochs required for downstream use, with any interpolation method and its validation recorded. Cartesian and explicitly defined radial, transverse, and normal frames support complementary uses. The axes, velocity convention, units, and complete transformation must be specified rather than inferred from an acronym. CCSDS Orbit Comprehensive Messages (OCMs) provide a serialization framework; the SDN profile must identify required standard fields and any linked metadata needed to convey distributional assumptions, process uncertainty, and calibration. ([R14](#r14))

Covariance is a prerequisite for covariance-based gates and collision-probability calculations, not for every uncertainty-aware method. The admissible-set and TEAG/ESPF evaluation paths in section 9 remain available. Each representation must carry its assumptions and demonstrate the coverage or enclosure properties it claims.

### 16.3 Observations and orbit determination

Observation-based estimation is the largest change from the recorded provider-product workflow. It requires an authorized data source, a validated measurement model, and an estimator appropriate to the measurement type.

ILRS full-rate and normal-point laser-ranging data provide a practical first path for range-model validation and batch correction from a known initial orbit. Normal points are condensed range observations; they are not automatically the angular measurements required for angles-only IOD. Station coordinates, timing, atmospheric corrections, calibration, and bias treatment must accompany the ranges. Dataset editions, access requirements, and processing conventions are frozen for each study. ([R23](#r23)) Laser ranging covers only retroreflector-equipped, mostly cooperative satellites, so it validates range models and estimation software on a population that is not representative of debris or other uncooperative ASOs.

An angles-only IOD study therefore needs a separate, identified angular dataset with station and timing metadata. A three-observation solver is an entry method, followed by geometry-dependent alternatives and additional observations where ambiguity remains. It is not a universal solver for every short arc or measurement type. For very short arcs, where classical three-observation methods are ill-conditioned, admissible-region methods constrain the unobserved range and range rate with physical bounds and can be carried forward as sets or as probabilistic mixtures ([R21](#r21), [R31](#r31)). This path gives the section 7 unassigned-observation hypothesis an actual solution procedure without requiring a provider element set.

Space-Track GP history supplies derived orbit products for consistency studies. It is not treated here as an identified public raw-observation archive. Any proposed Space-Track observation source must first be named, its access established, and its measurement content verified. ([R24](#r24))

Batch differential correction then compares predicted observables with measurements under the section 8 dynamics and sensor models. It must document weights, residual rejection, bias treatment, parameter observability, convergence, and uncertainty construction. Publish the fit arc, forecast validity interval, acceptance criteria, and diagnostics. Observation-based estimation adds to, rather than removes, the provider-position reconciliation path needed when measurements are unavailable. Initial-state covariance from a fit remains conditional on its assumptions until tested on held-out evidence.

### 16.4 Association and prediction-error models

SDN requires prediction-age-dependent association gates, configurable discrimination, and explicit object lifecycle states. Gate parameters must be learned and validated for the catalog's own inputs rather than inherited from another provider.

Differences between a TLE propagated forward and a later-issued solution provide an empirical measure of inter-solution consistency. They are a useful starting proxy for prediction-error envelopes, but are not identical to physical prediction errors: both solutions are uncertain and may share observations or model biases. The TLE-based fitting literature supports evaluating derived forecasts, not treating every later TLE as truth. ([R18](#r18))

Calibrate these proxies against held-out observations or independently characterized reference states, stratified by orbit regime, prediction age, and source lineage. An observation-space chi-square gate additionally requires the measurement model, an innovation covariance combining predicted and measurement uncertainty, appropriate correlations, and a stated distributional approximation. Empirical or bounded-set gates remain alternatives when those assumptions are unsupported.

Keep section 7's separation between candidate generation, discrimination, and publication decisions. Record compatible, rejected, ambiguous, and insufficient outcomes. After an observation gap, reacquisition may retain an established identity when evidence supports it. A confirmed physical reentry is terminal; an erroneous decay classification can instead be corrected through the reversible record. Loss of tracking and physical decay must not be conflated.

### 16.5 Maneuver detection and lifecycle

Use residual analysis across adjacent arcs to propose maneuver hypotheses. A change point may also reflect sensor bias, source changes, association errors, or model discrepancy, so detection is a prescreen rather than proof of a burn. Compare plausible explanations and retain rejected or unresolved hypotheses.

An accepted maneuver hypothesis records event time, estimated velocity change or finite-burn model, supporting evidence, and uncertainty in the dynamics reference. This makes the discontinuity auditable instead of silently starting a new arc. Distinguish propagation termination thresholds from confirmed decay evidence. An open catalog needs explicit lost, inactive, reentered, and uncertain statuses rather than inferring physical events from a provider product's disappearance.

### 16.6 Accuracy assessment

A continuing accuracy-assessment loop is central to this roadmap. Predicted products are checked against subsequent evidence, with results reported per object, prediction age, and orbit regime. The presence of accuracy-record fields alone does not establish the quality of the underlying calibration.

SDN must distinguish three outputs. Held-out measurement residuals test predictions in observation space. Independent reference-state comparisons support position and velocity error estimates, subject to reference uncertainty and lineage. Encounter-level comparisons assess closest-approach time and miss distance when an adequately characterized encounter reference exists. A conjunction event or covariance by itself supplies no measured physical error, and a single range residual does not determine all three Cartesian error components.

Publish these results beside the trajectory, with failure counts retained in the denominator and fit data separated from forecast evaluation. Test covariance coverage or admissible-set enclosure under the stated assumptions. A formal accuracy certification would additionally require a named acceptance procedure, thresholds, evaluation population, and review authority; the present proposal establishes an assessment program, not a completed certification.

### 16.7 Mapping SP HAC capabilities to the open SDN catalog

The path from Special Perturbations (SP) and the High Accuracy Catalog (HAC) capability baseline to an open SDN catalog begins by making the dynamics reproducible. We map the closed-system capability categories to explicit open model components, data dependencies, and acceptance tests. The mapping defines engineering targets; it does not establish that every listed option is active in a particular operational configuration. The open implementation can use different software while preserving the physical model and its declared conventions.

The principal dynamics gaps are identifiable. Earth gravity, atmospheric drag, radiation pressure, third-body gravity, tides, maneuvers, numerical integration, and reference-system transformations all have implementable open counterparts. Availability of a counterpart does not establish equivalence to a particular configuration. The following mapping specifies what SDN must preserve or explicitly substitute.

| Closed capability target | Open SDN counterpart | Equivalence condition |
| --- | --- | --- |
| Configurable geopotential | Spherical harmonics using EGM96 or EGM2008, with independently selected degree and order | Same coefficients, normalization, gravity constant, reference radius, and tide system |
| Legacy gravity selections | Versioned coefficient loaders for WGS72/WGS84-associated sets, JGM2, GEM5/GEM9, NWL8C, and SEM68R where available | Exact coefficient edition and conventions established; a model label alone is insufficient |
| SP modified Jacchia 70 density | Open implementation of the corresponding modified model | Match the specific modifications, environmental inputs, and correction handling; standard Jacchia 70 is not presumed equivalent |
| MSIS 90 density | Version-specific atmosphere adapter with environmental-data inputs | Same density routine, switches, drivers, and time conventions; newer models are declared substitutes |
| Hybrid atmosphere selection | Explicit routing among density routines | Known selection or blending rule, transition behavior, and input handling |
| Atmospheric drag | Relative-atmosphere velocity with declared drag coefficient, area, and mass | Same density, rotation or wind assumptions, units, and ballistic-parameter definition |
| Solar radiation pressure | Cannonball or attitude-dependent radiation model with eclipses | Same optical properties, area-to-mass ratio, irradiance, and shadow geometry |
| Sun and Moon perturbations | Differential third-body acceleration from versioned ephemerides | Same body positions, gravitational constants, frames, and time arguments |
| Solid Earth and ocean tides | Time-varying geopotential corrections | Same tidal constituents, response parameters, and permanent-tide convention |
| Thrust and outgassing | Impulsive or finite-burn models and evidence-supported empirical accelerations | Explicit event times, local or inertial directions, units, mass treatment, and parameter uncertainty |
| Numerical propagation | Validated equations of motion, adaptive integration, and event handling | Converged trajectories under matched dynamics, with declared error controls |
| Frames and time | Explicit inertial and terrestrial transformations with versioned Earth orientation and leap seconds | Epoch, time scale, frame realization, and full position-and-velocity transformation agree |

For gravity, a 24-by-24 or 70-by-70 configuration is a truncation choice, not a separate physical model. Load the actual coefficient set and record its hash, maximum degree and order, normalization, gravitational constant, and reference radius. Public EGM96 and EGM2008 coefficients support this approach. Legacy names require the same discipline: an ellipsoid or reference-system label does not uniquely specify a gravity coefficient file. Where the required edition cannot be obtained and verified, label the replacement as a substitute and leave equivalence unmeasured. ([R26](#r26))

SP's modified Jacchia 70 is the compatibility target for that atmosphere option, not generic Jacchia 70. The open implementation must match the applicable modifications, environmental-input conventions, and correction handling. A public implementation labeled "modified Jacchia 70" is not automatically the same variant. Standard Jacchia 70 or another public atmosphere model remains a declared substitute until density and drag-acceleration comparisons establish agreement over the stated test domain.

Reproducing the modified density routine does not by itself reproduce an operational density-calibration process. Treat any separately applied correction fields or calibration products as explicit, versioned dependencies, with their availability and permitted use established. Public HASDM literature ([R28](#r28), [R32](#r32)) describes a modified Jacchia-70 model and estimated temperature corrections; it provides context, not proof of the exact SP version or configuration being compared. Where the required variant or correction inputs are unavailable, retain the open substitute and mark SP equivalence as unmeasured. ([R28](#r28)) Public research describes HASDM using JB2008 as its background density model, with dynamic corrections estimated through calibration-satellite observations ([R33](#r33)). The open mapping therefore includes both the modified Jacchia 70 option and the publicly described JB2008-based HASDM framework; reproducing either base model alone does not reproduce the calibration process.

Atmospheric parity requires more than choosing a density-model family. SP's modified Jacchia 70, standard Jacchia 70, MSIS 90, NRLMSISE-00, and JB2008 are distinct compatibility targets. Each adapter must define altitude and latitude conventions, local solar time, solar and geomagnetic inputs, averaging windows, lags, switches, interpolation, and missing-data behavior. F10.7 and Ap alone do not supply all JB2008 inputs; S10, M10, Y10, their averages, and the geomagnetic temperature correction also matter. A hybrid mode requires an explicit selection or blending rule rather than an assumed altitude threshold. ([R25](#r25), [R27](#r27))

Drag configuration must state whether a supplied ballistic parameter means mass divided by drag coefficient and area, or the reciprocal coefficient-area-to-mass quantity. These conventions cannot be interchanged by relabeling units. Space-weather radio-flux indices used by an atmosphere model are also distinct from the photon irradiance used for radiation pressure. The basic cannonball radiation acceleration points away from the Sun and requires a declared eclipse model. Force and acceleration must remain distinct when converting input units.

Sun and Moon gravity uses time-dependent ephemerides and the differential acceleration appropriate to an Earth-centered origin, not fixed mean distances. Tidal geopotential corrections belong in the dynamics; station displacement and loading corrections belong in the measurement model and must be handled separately where required. Record the adopted conventions and data editions. ([R11](#r11))

For maneuvers, an in-track direction is defined by the object's orbital frame and changes relative to inertial axes. It cannot be represented by an unexplained fixed inertial unit vector. Specify finite-burn timing, thrust or acceleration magnitude, direction convention, and mass evolution where relevant. Enable an outgassing or empirical acceleration model only with a stated physical or estimation rationale; assumed defaults must not become undeclared evidence about an object.

Numerical equivalence does not require copying a proprietary integrator. Cowell and Encke describe formulations of the equations of motion; a Runge-Kutta method describes an integration algorithm. SDN must name both the formulation and algorithm, define dimensional absolute tolerances and relative tolerances, and verify convergence, interpolation, and event handling. A tight tolerance setting alone is not an accuracy result. The publicly described SP propagator applies an eighth-order Gauss–Jackson multistep method to the Cowell formulation ([R29](#r29), [R30](#r30)). Its behavior on eccentric orbits depends on step-size control or regularization and on the startup procedure, so integrator comparisons should include high-eccentricity and perigee-passage cases and should report agreement separately from the force-model comparisons.

Frame and time adapters must preserve the exact epoch origin, units, and time scale of each input. Use epoch-appropriate Earth-orientation parameters and leap-second data rather than fixed example offsets. J2000, ICRF, TEME, and terrestrial frame labels cannot be treated as interchangeable. Transform velocity consistently with the time-dependent frame rotation, and distinguish sidereal angles from time scales. ([R11](#r11))

Element-set semantics retain an explicit dispatch guard. The section 5 SGP4 handoff applies to SGP4-compatible mean elements, not every record shaped like a two-line set. Verify each source's ephemeris type, element definitions, frame, time scale, and generating model; route it to a compatible decoder or refuse it. Passing osculating elements to an SGP4 mean-element decoder is a model mismatch, not a valid handoff. Checksums establish record integrity, not model compatibility. A valid TLE-to-state handoff remains a legitimate way to initialize this open numerical pipeline; the additional models do not by themselves establish a better forecast.

### 16.8 From model mapping to published catalog records

The SDN bridge should expose a versioned dynamics configuration rather than bind the catalog schema to one analysis library. Each configuration identifies the force models, coefficient and environmental-data hashes, frame and time conventions, object parameters and their treatment, integrator settings, and supported interval. Mark each mapped component as a matched implementation, a declared substitute, or unresolved. A match claim must link to its test evidence; it is not inferred from a shared model name.

An ingestion adapter preserves the original provider product and constructs a canonical state only after source-model dispatch. The propagation service consumes that state and the dynamics configuration. Observation-based estimation can then update the state and selected parameters using an explicit measurement model. The uncertainty service transports the chosen covariance or admissible representation with compatible dynamics. Finally, the catalog publishes an attributed solution, ephemeris, uncertainty status, validation interval, and evidence links under the section 2 contracts. New solutions do not erase the original claim or its lineage.

This separates access to a closed product from dependence on a closed implementation. An authorized SP HAC reference product can support comparison without becoming the SDN runtime or being redistributed. Where only a state or ephemeris is available, SDN can test trajectory agreement but cannot infer the generating force configuration uniquely. Where observations are available, SDN can independently estimate and validate its own solution. Where neither matching inputs nor reference outputs are available, the corresponding parity claim remains open.

The dynamics mapping principally closes G7 and G9. It supplies a foundation for G1 and G2, but it does not itself deliver calibrated uncertainty, raw observations, association, maneuver inference, or conjunction assessment. Those remaining capabilities follow sections 16.2 through 16.6. Reproducing a propagator is one part of building the catalog; comparable catalog performance also depends on observation coverage, cadence, calibration, and operational continuity.

The availability of observations, sensor calibrations, provider-specific environmental inputs, and reference products must be established source by source. Public and participating-sensor datasets have their own coverage, errors, and dependencies. Public standards and reproducible independent tests provide the engineering basis, with access rights and reference independence recorded for every study.

### 16.9 Sequencing and acceptance gates

Begin with source-model dispatch, units, frames, time, and the versioned dynamics configuration. Keep the recorded SDN baseline reproducible while adding mapped components incrementally. A useful progression is gravity and ephemerides, atmosphere and drag, radiation and eclipses, then tides and event-driven accelerations where sensitivity justifies them. Observation and uncertainty development can proceed in parallel.

| Acceptance stage | Required evidence | Supported claim |
| --- | --- | --- |
| Input and convention checks | Round-trip transforms, epoch tests, coefficient checks, and parameter-unit checks | Inputs mean the same thing in both implementations |
| Component comparisons | Force-by-force accelerations at fixed states and epochs, including boundary cases | Tested components agree within stated tolerances |
| Matched propagation | Common initial state, matched configuration, identical output epochs, and tolerance convergence | Trajectories agree over the tested interval |
| Estimation and uncertainty | Declared observations, fit controls, parameter treatment, and calibration tests | State and uncertainty products support their stated interpretation |
| Forecast and catalog assessment | Held-out evidence, regime and age stratification, coverage, and failure counts | Performance supports a declared catalog use and interval |

Use authorized closed-system outputs for matched comparisons when available and independent open reference implementations for cross-checks. Freeze the initial state during force and propagation comparisons; refitting it can conceal a model mismatch. Test refitting as a separate estimation experiment. Set acceptance tolerances before examining results, using the intended application and uncertainty budget, and report both position and velocity differences. Include drag-sensitive, eclipse-crossing, eccentric, and maneuver cases where those capabilities are claimed.

Develop STM transport with declared test covariances while establishing defensible initial uncertainty through provider information, empirical calibration, or observation-based estimation. SLR range validation and batch correction form one path; angular-data acquisition and angles-only IOD form another. Next integrate association, maneuver hypotheses, and lifecycle handling. Add CDM serialization and uncertainty-aware encounter assessment as the required inputs become available.

Continuous accuracy testing starts with the first testable products. Capability coverage, numerical agreement with a reference implementation, and accuracy against independent evidence are separate acceptance results. A declared substitute may improve physical forecasts while reducing agreement with a closed reference; an exact numerical match may preserve a shared bias. Publish those outcomes separately rather than calling either one universal parity.

## Appendix A. Glossary and nomenclature

### Terms

| Term | Meaning |
| --- | --- |
| ASO | Anthropogenic space object |
| Admissible set | States, parameters, and trajectories consistent with the evidence under declared assumptions and error allowances |
| Provider claim | An attributed estimate (ephemeris, element set, identity link) that already embeds a provider's dynamics model, measurement model, and inference |
| Observation | A direct measurement such as angles, range, range rate, or photometry |
| Level 0 to Level 3 | Processing levels of orbit data: raw sensor data, calibrated measurements, estimated states and ephemerides, and averaged theory-specific elements (section 1) |
| Nominal, ordinal, interval, ratio | Levels of measurement: labels; order only; differences with an arbitrary zero; differences and ratios with a true zero |
| Directional (circular) scale | Angular values that wrap, requiring wrapped differences and circular statistics |
| Mean elements | Averaged elements defined only within a specific analytic theory such as SGP4 |
| Osculating elements | Instantaneous two-body elements matching a state at one epoch |
| Quantization | The resolution limit imposed by a fixed-width numeric format |
| Contributor | One of the five sources of evidential support for an orbit solution (section 1) |
| Observability report | Statement of which state and physical parameters the evidence constrains |
| TLE-seeded product | A numerical trajectory initialized from an SGP4 epoch state |
| Initial-condition uncertainty | The set or distribution of epoch states not ruled out by the source product, carried into propagation |
| Possibility | Degree to which a hypothesis is compatible with the evidence |
| Necessity | Degree of support for a hypothesis through exclusion of alternatives |
| TEAG | Theory of Epistemic Abductive Geometry |
| ESPF | Epistemic Support-Point Filter |
| HPOP | High-precision (numerical) orbit propagation |
| SGP4, TLE, $B^*$ | Simplified General Perturbations 4 theory; two-line element set; SGP4 drag term |
| GP, OMM, OPM, OEM | General perturbations element set; CCSDS Orbit Mean-elements, Parameter, and Ephemeris Messages |
| TEME, GCRF, J2000 | True Equator Mean Equinox frame; Geocentric Celestial Reference Frame; J2000 reference frame |
| UTC, TDB | Coordinated Universal Time; Barycentric Dynamical Time |
| SOCRATES | CelesTrak conjunction screening service |
| AOE, KY | Space Mapper's self-determined orbit family and its API channel |
| IOD | Initial orbit determination: deriving a first state from a short arc of observations |
| CODAC, SuperCODAC, SCCAT | Space Force applications |
| STM | State-transition matrix; linear map propagating state deviations and covariance |
| SuperCOMBO | Space Force application for conjunction analysis |
| CAWR | Conjunction-analysis report product associated with SuperCOMBO |
| CDM | CCSDS Conjunction Data Message |
| ASW | Astrodynamics Support Workstation, the U.S. Space Force astrodynamics environment named in public literature ([R29](#r29)) |
| SP, HAC | Special Perturbations numerical orbit determination and propagation; High Accuracy Catalog |
| HASDM | High Accuracy Satellite Drag Model: dynamic density corrections estimated from calibration-object tracking; public descriptions include Jacchia 70 and JB2008 background models |
| Jacchia 70, MSIS 90, NRLMSISE-00, JB2008 | Empirical thermospheric density models; each is a distinct compatibility target (section 16.7) |
| EGM96, EGM2008 | Earth Gravitational Models published by NGA as spherical-harmonic coefficient sets |
| Gauss–Jackson | Fixed-order multistep integrator for second-order equations of motion, used with the Cowell formulation |
| Cowell, Encke | Formulations of the equations of motion: direct integration of total acceleration; integration of deviations from a reference orbit |
| RKF78 | Runge–Kutta–Fehlberg 7(8) adaptive integrator used in the Vimpel study |
| Kozai, Brouwer mean motion | Two mean-motion conventions; TLEs carry Kozai values, which SGP4 converts to Brouwer values internally |
| Consider parameter | A parameter that is not estimated but whose uncertainty is included in the solution covariance |
| SLR, ILRS | Satellite laser ranging; International Laser Ranging Service |
| CPF | ILRS Consolidated Prediction Format |
| OCM | CCSDS Orbit Comprehensive Message |
| IPFS | InterPlanetary File System, a content-addressed distribution network |
| GOST | Russian state standard; here the GOST upper-atmosphere density model associated with Vimpel’s published dynamics |
| G1–G9 | Capability gaps enumerated in section 16.1 |

### Symbols

| Symbol | Meaning |
| --- | --- |
| a, e, i | Semimajor axis, eccentricity, inclination |
| $\Omega$, $\omega$ | Ascending-node longitude, argument of perigee |
| u, $\nu$ | Argument of latitude, true anomaly |
| M, $\Delta M$ | Mean anomaly; its format resolution |
| n | Mean motion |
| p (section 4) | Semilatus rectum |
| $\mu$ | Earth's gravitational parameter |
| $\mathbf{r}_{\mathrm{pf}}$, $\mathbf{v}_{\mathrm{pf}}$ | Perifocal position and velocity |
| $R_x$, $R_z$, Q | Elementary rotations; perifocal-to-J2000 rotation |
| $\mathbf{r}_{\mathrm{J2000}}$, $\mathbf{v}_{\mathrm{J2000}}$ | J2000 position and velocity |
| r, $\dot{\mathbf{r}}$, $\ddot{\mathbf{r}}$ | Object position, velocity, acceleration |
| t | Time |
| $\mathbf{a}_{\mathrm{gen}}$, $\mathbf{a}_{\mathrm{obj}}$ | Shared-physics and object-specific accelerations |
| p (sections 8 and 9) | Object physical parameter vector |
| $\delta(t)$ | Residual dynamics-model discrepancy |
| y, h | Observable; measurement model |
| x, s | Object state; sensor state and calibration |
| b, e, $\varepsilon$ | Modeled bias; random error; residual measurement-model discrepancy |
| t₀, P₀, P(t) | Initial epoch; initial-state covariance; propagated covariance (section 16.2) |
| Φ(t, t₀) | State-transition matrix from t₀ to t |
| Q (section 4); Q(t) (section 16.2) | Perifocal-to-J2000 rotation matrix; accumulated process covariance. Distinct quantities |
| e (sections 4–5); e (section 8) | Eccentricity; random measurement error. Distinct quantities |
| a; a with subscripts | Semimajor axis; accelerations a_gen and a_obj (section 8). Distinct quantities |
| A, Aᶜ, Π, N | Event, its complement, possibility measure, necessity measure (section 12) |

## References

R1–R15 retain the sources and evidence roles of the 21 September 2026 baseline. Provider API descriptions refer to that reviewed baseline, not a fresh acquisition. R16–R20 support the added discussion of model transfer, estimation, and proposed inference methods. R21 and R22–R25 support initial orbit determination, covariance propagation, measurement-data distinctions, and atmospheric inputs. R26–R32 support the section 16 mapping to the publicly documented U.S. Space Force baseline. Restricted source records and credentials are not reproduced.

### R1

Edgesource. Epoch-state conversion, validation and refinement, with aggregate verification record. Modules commit 49e159d7003cac3d3e65b03170f11909fa86dd2c. [Method](https://github.com/DigitalArsenal/space-data-network-modules/blob/49e159d7003cac3d3e65b03170f11909fa86dd2c/analysis/catalog-composer/docs/epoch-fitting.md) · [Verification record](https://github.com/DigitalArsenal/space-data-network-modules/blob/49e159d7003cac3d3e65b03170f11909fa86dd2c/analysis/catalog-composer/docs/verification-vimpel-epoch-fit-20260921.json)

### R2

Edgesource. Catalog Editor module: composition, coverage and matching contracts. Same baseline commit. [Module documentation](https://github.com/DigitalArsenal/space-data-network-modules/blob/49e159d7003cac3d3e65b03170f11909fa86dd2c/analysis/catalog-composer/README.md)

### R3

Edgesource. Vimpel epoch normalization and catalog matching; 64-object diagnostic audit. Same baseline commit. [Normalization](https://github.com/DigitalArsenal/space-data-network-modules/blob/49e159d7003cac3d3e65b03170f11909fa86dd2c/analysis/catalog-composer/docs/vimpel-normalization.md) · [Audit record](https://github.com/DigitalArsenal/space-data-network-modules/blob/49e159d7003cac3d3e65b03170f11909fa86dd2c/analysis/catalog-composer/docs/vimpel-epoch-audit-20260921.json)

### R4

Space Mapper. AOE Catalog. Provider description. [AOE Catalog](https://spacemapper.cn/en-us/catalog/aoecat/)

### R5

Space Mapper. Orbital Database. Standard, AOE, and international product families. [Orbital products](https://spacemapper.cn/en-us/satellite/orbital)

### R6

Space Mapper. Orbit-list API documentation. Channels, formats, and metadata examples. [Orbit API](https://spacemapper.cn/help/helpinfo/1613/)

### R7

Edgesource. Ephemeris provider test fleet. Historical development verification at stack snapshot 43f5506457; transport evidence and acquisition limitations. [Provider-fleet report](https://github.com/DigitalArsenal/spacedatanetwork-stack/blob/43f5506457/studies/orbital-console-data/deployment/EPHEMERIS-PROVIDERS.md)

### R8

International Laser Ranging Service, NASA GSFC. Consolidated Prediction Format, version 2, and supporting material. [CPF documentation](https://ilrs.gsfc.nasa.gov/data_and_products/formats/cpf.html)

### R9

Space Mapper. Space-object API documentation. AOE, INTL, and MIXED sources and identifiers. [Catalog API](https://spacemapper.cn/help/helpinfo/1621/)

### R10

JSC Vimpel and Keldysh Institute of Applied Mathematics. Orbit parameters of newly detected HEO space debris objects. Public bulletin and format explanation; retained study interpretation in ([R3](#r3)). [Provider bulletin](https://spacedata.vimpel.ru/en/)

### R11

Petit, G., and Luzum, B., editors. IERS Conventions (2010). IERS Technical Note 36. [Technical note](https://iers-conventions.obspm.fr/conventions/content/tn36.pdf)

### R12

CelesTrak. Current Supplemental GP Element Sets. Methodology for fitting operator ephemerides with SGP4. [Supplemental GP methodology](https://www.celestrak.org/NORAD/elements/supplemental/)

### R13

Vallado, D. A., Crawford, P., Hujsak, R., and Kelso, T. S. Revisiting Spacetrack Report #3. AIAA 2006-6753. SGP4 theory, verification, frame conventions, and model-dependent estimation cautions. [Paper](https://celestrak.org/publications/AIAA/2006-6753/AIAA-2006-6753.pdf)

### R14

Consultative Committee for Space Data Systems. Orbit Data Messages. CCSDS 502.0-B-3, Issue 3, May 2023, including listed corrigenda. [Publication record](https://ccsds.org/publications/allpubs/entry/3073/)

### R15

CelesTrak. SOCRATES Plus. Methodology and service description; cited as documentation, not a completed SDN parity result. [SOCRATES methodology](https://celestrak.org/SOCRATES/)

### R16

Orekit developer discussion. Using NRLMSISE-00 with numerical propagator. 3 August 2022. Clarifies the SGP4 initial state and its use to initialize numerical propagation. [Developer explanation](https://forum.orekit.org/t/using-nrlmsise-00-with-numerical-propagator/1875)

### R17

Orekit. Orbit Determination architecture. Measurement modeling, propagation, residuals, and estimation. [Estimation architecture](https://www.orekit.org/site-orekit-11.0.1/architecture/estimation.html)

### R18

Levit, C., and Marshall, W. Improved orbit predictions using two-line elements. Advances in Space Research 47(7), 1107–1115, 2011; arXiv:1002.2277. Numerical fitting to states from successive TLEs and tested forecast improvements. [Research paper](https://arxiv.org/abs/1002.2277)

### R19

Jah, M. K. Theory of Epistemic Abductive Geometry (TEAG): A Unified Theory of Admissibility-Driven Inference Across Dynamical Systems, Measure Theory, and Language. Preprint, version 3, 2026. Proposed inference framework. [Versioned preprint](https://doi.org/10.20944/preprints202603.2010.v3)

### R20

Jah, M. K. The Epistemic Support-Point Filter as a Tropical Hamilton–Jacobi System: Wavefront Propagation and Possibilistic Inference. Preprint, version 3, 2026. Proposed filtering framework. [Versioned preprint](https://doi.org/10.20944/preprints202603.2110.v3)

### R21

Milani, A., Gronchi, G. F., de’ Michieli Vitturi, M., and Knežević, Z. Orbit determination with very short arcs. I: Admissible regions. Celestial Mechanics and Dynamical Astronomy 90, 57–85, 2004. Admissible-region construction for too-short arcs. [Research paper](https://ui.adsabs.harvard.edu/abs/2004CeMDA..90...57M)

### R22

Orekit. StateCovarianceMatrixProvider. Initial covariance and state-transition-matrix propagation. [API documentation](https://www.orekit.org/site-orekit-12.2/apidocs/org/orekit/propagation/StateCovarianceMatrixProvider.html)

### R23

International Laser Ranging Service. Laser ranging data: full-rate and normal-point observations. [Data description](https://ilrs.gsfc.nasa.gov/data_and_products/data/index.html)

### R24

Space-Track.org. API and data documentation, including GP and GP history products. [Documentation](https://www.space-track.org/documentation)

### R25

Orekit. JB2008 atmosphere model implementation and model-specific environmental inputs. [Implementation](https://www.orekit.org/site-orekit-13.1.1/jacoco/org.orekit.models.earth.atmosphere/JB2008.java.html)

### R26

National Geospatial-Intelligence Agency. Earth Gravitational Models EGM96 and EGM2008. Public coefficient products and model definitions. [Gravity models](https://earth-info.nga.mil/?action=wgs84&dir=wgs84)

### R27

Picone, J. M., Hedin, A. E., Drob, D. P., and Aikin, A. C. NRLMSISE-00 empirical model of the atmosphere: Statistical comparisons and scientific issues. Journal of Geophysical Research 107(A12), 1468, 2002. Distinguishes model generations and environmental responses. [Research paper](https://doi.org/10.1029/2002JA009430)

### R28

Tobiska, W. K. E10.7 Use for Global Atmospheric Density Forecasting in 2001. AIAA 2002-4892, 2002. Public discussion of the modified Jacchia-70 model and atmospheric temperature corrections in HASDM forecasting; not identification of a particular SP software configuration. [Technical paper](https://spacewx.com/wp-content/uploads/2020/11/2002_4892.pdf)

### R29

National Research Council. Continuing Kepler’s Quest: Assessing Air Force Space Command’s Astrodynamics Standards. The National Academies Press, Washington, DC, 2012. Public description of SP, HASDM, the ASW, and covariance realism; not a statement of current operational configuration. [Report](https://doi.org/10.17226/13456)

### R30

Berry, M. M., and Healy, L. M. Implementation of Gauss–Jackson Integration for Orbit Propagation. Journal of the Astronautical Sciences 52(3), 331–357, 2004. Gauss–Jackson formulation, startup, and step control. [Research paper](https://ui.adsabs.harvard.edu/abs/2004JAnSc..52..331B/abstract)

### R31

DeMars, K. J., and Jah, M. K. Probabilistic Initial Orbit Determination Using Gaussian Mixture Models. Journal of Guidance, Control, and Dynamics 36(5), 1324–1335, 2013. Admissible-region initialization carried forward as a probabilistic mixture. [Research paper](https://doi.org/10.2514/1.59844)

### R32

Storz, M. F., Bowman, B. R., Branson, J. I., Casali, S. J., and Tobiska, W. K. High Accuracy Satellite Drag Model (HASDM). Advances in Space Research 36(12), 2497–2505, 2005. Dynamic calibration of Jacchia 70 density from calibration-satellite tracking. [Research paper](https://ui.adsabs.harvard.edu/abs/2005AdSpR..36.2497S/abstract)

### R33

Licata, R. J., Mehta, P. M., Tobiska, W. K., Bowman, B. R., and Pilinski, M. D. Qualitative and Quantitative Assessment of the SET HASDM Database. Space Weather 19, e2021SW002798, 2021. Describes JB2008 as the HASDM background density model and dynamic calibration. [Research paper](https://doi.org/10.1029/2021SW002798)

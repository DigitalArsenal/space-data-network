# Evidence-Supported ASO Catalog

A measured orbital catalog for Space Data Network

Anthony "TJ" Koury III and Claude Opus 5.5

Koury: Edgesource, Space Data Network. Claude: Anthropic.

Technical whitepaper 2.4 | 16 October 2026

Numerical evidence cutoff: 11 October 2026

## 1 Summary

Space Data Network (SDN) builds a catalog of anthropogenic space objects (ASOs) from public and login-only sources. Object by object, it publishes the solution with the best measured accuracy for the intended use, with its uncertainty. Accuracy is measured against independent precise orbits, under plans fixed before the test data were read<sup>[1](#r1)</sup>. Each method is chosen and tuned on one set of data, the selection window (also called the training window), and scored on held-out data: observations set aside until the method is fixed, either a later time window or objects the tuning never saw. A result on held-out data shows how the method performs on cases it has not seen, not how well it fits the data it was tuned on. Section 2 states the method step by step, with the rule that gives every object a covariance.

| Object class | Catalog product | Measured (median) |
| --- | --- | --- |
| GPS satellites | Analysis-centre orbit, propagated | 3.5 cm at issue, 30 m a day later |
| Satellites with laser ranging | High-precision orbit propagation (HPOP) fitted to the ranges | 7 m at the fit, 42 m after 7 days |
| Satellites with operator ephemerides | The operator's ephemeris | Public element sets (OMMs) lie 0.6–5 km from it |
| Objects known only from public OMMs | SGP4 on the OMM | 0.3–1.6 km at issue |

For most objects the only public input is the OMM, and the catalog takes it as it is. What the catalog adds is a measured account of each input's error, and screening that acts on it safely:

1. **Measured error as covariance.** An orbit propagated from an OMM's epoch state carries a covariance equal to the error measured for its orbit regime and element-set age. That error is measured against independent orbits in every regime that has them: GNSS analysis-centre orbits, laser ranging, LEO precise orbits, operator ephemerides and radio-occultation receivers. A covariance is labeled calibrated only where held-out data confirm it (section 2.2).
2. **SDN's own atmosphere.** Drag uses JB2008 or NRLMSISE-00 with an open correction in the form of the Space Force's Dynamic Calibration of the Atmosphere, solved from public operator states. It explains 25–40 % of the density variation measured by satellites that did not calibrate it; 1–3-day position error is unchanged (ratios 0.98–1.02) (section 5.4).
3. **Maneuvers.** A detector reads each object's element-set history for steps in orbital energy and plane, and reports each maneuver's time and size. On held-out data it found 22 of 25 ISS burns and all 6 GPS maneuvers in precise orbits; geostationary station-keeping is harder, 4 of 11 (section 5.5).
4. **Better start states where data allow.** HPOP's force models agree with Orekit within 1 cm over a day (steps of at most 30 s; 2.5 cm at 300 s, section 7). Fitted to laser ranges, HPOP stays 20–60 times better than SGP4 to 7 days; fitted to an arc of corrected OMMs, it halves SGP4's error to two days in GPS (section 5.3). In LEO, drag from B* limits even a precise start state: 481 m at 24 h (section 5.3). SGP4-XP sets that SDN fits to precise GNSS orbits err by a median 0.42 km over the seventh day, against 14 km for its SGP4 fits of the same orbits (exploratory, section 4).
5. **Screening when the covariance is large.** A large covariance spreads a collision's probability thin. In a synthetic study, with collisions built on the OMM errors of 4 reference objects, a probability threshold of 1e-4 missed 2.7–16 % of collisions; screening on the uncertainty region missed 0.2–0.4 %, with 21–45 % false alerts at a 1 km miss<sup>[50](#r50)</sup>. Every event also carries the Alfano maximum, the largest probability a spherical covariance of any size gives, which needs no covariance. Products built from public OMMs are advisory: public element sets alone must not drive conjunction assessment decisions<sup>[49](#r49),[53](#r53)</sup>.

Three results go beyond published work:

1. An element-set correction learned on a LEO fleet carries to that fleet's unseen satellites (25 % lower error at epoch) but not to other operators' satellites, nor across GPS orbital planes; in GNSS a per-satellite bias holds 37 % of the along-track variance, against 1–5 % in LEO (section 5.3).
2. A density correction of the kind the Space Force's drag model uses, solved from Planet's public states, is scored on satellites that did not calibrate it (section 5.4).
3. Consecutive OMM errors are strongly correlated, and four published covariance constructions are overconfident (section 6).

Pre-registered hypotheses that failed are reported as not supported.

## 2 Method

The catalog is assembled object by object from the products of every source. Each object ends with a state, a covariance and its maneuvers.

### 2.1 Construction

1. **Read each source at its level.** Each record becomes the Space Data Standards (SDS) record of its kind, under the source's own identifier: element sets as OMMs, ephemerides as OEMs, laser normal points and other measurements as observations (sections 3 and 4).
2. **Derive the same products from every source.** For every object a source covers, the method derives four records:
   1. An SGP4 element set (OMM): the source's own, or one fitted to the source's ephemeris (section 4).
   2. An SGP4-XP element set: an OMM in the extended-perturbation theory of the Space Force's Astrodynamics Standards<sup>[52](#r52)</sup>, with its ballistic and radiation coefficients, fitted to the same data (section 4).
   3. A state with its covariance (OCM): the source's state at its epoch, or the SGP4 epoch state of the handoff in section 4, with the covariance of section 2.2.
   4. The maneuvers in the source's history (MNV), from the detector of section 5.5.

   Sources are then compared like for like, and each can be propagated and shown on its own.
3. **Measure each source.** Each product is scored against independent precise orbits by object class, orbit regime and product age, under a plan fixed before its test data were read (sections 5 and 10). A source's measured errors form its error model.
4. **Associate.** Crosswalks propose links between records of one object; ephemerides compared over shared intervals accept, reject or hold each link (section 8).
5. **Select.** For each object, regime and horizon, the catalog takes the product of the source with the lowest measured median error on a training window of at least 30 samples. Fusing sources was never better than selecting one (section 8).
6. **Attach covariance and maneuvers.** The selected product keeps its covariance and label (section 2.2). The object carries the maneuvers of all its sources, merged by time and size. A product whose epoch precedes a maneuver does not describe the orbit after it, so selection after a maneuver draws only on later products.
7. **Write the catalog.** A catalog record (CAT) per object, with its selected OMM, SGP4-XP OMM and OCM and its merged MNV records, each naming the source it came from.

### 2.2 Covariance for every object

Every object carries a covariance, from the first rule that applies:

1. **The operator supplies one.** The operator's covariance, as supplied.
2. **An orbit fit made the product.** The fit's formal covariance, scaled by the realism factor measured for that source and object class on held-out data. Unscaled, laser-range fits are 16–21 times too small in mean d²/3 (section 6).
3. **The source has an error model for the object's regime.** The source's measured error at the element set's age, mapped from radial, in-track and cross-track axes to Cartesian. The model is built from the errors of consecutive element sets against independent precise orbits, with their correlation, and calibrated by regime and age (section 6). AOE's sets match Space-Track's error at equal age (section 4) and take its model.
4. **Otherwise.** The same model pooled over all sources in the object's regime.

Rules 3 and 4 cover every object with an element set, so no object goes without a covariance.

Each covariance carries a label: the rule, the source, the regime and age cell, and the verdict of held-out data for that cell. Calibrated means the realism rule of section 6 held on held-out data: mean d²/3 in [0.8, 1.25] and 95 % coverage in [0.93, 0.97]. Otherwise the label records the measured mean d²/3 and coverage. The element-set model is calibrated in 2 of 35 regime-and-age cells, LEO above 800 km at issue and at half a day; the HPOP epoch state with per-regime P₀ and Q in 5 (section 6). Screening on the uncertainty region relies on calibrated cells. Every screening event also carries the Alfano maximum, the largest probability a spherical covariance of any size gives, which needs no covariance<sup>[50](#r50)</sup>. It is not a bound for other shapes: at aspect ratios of 3, 10 and 100 to 1,000 the largest probability is about 3, 10 and 57 to 131 times higher, and LEO element-set covariances have aspect ratios of 2.4 to 191.

Covariance propagates through HPOP's state-transition matrix with per-regime process noise, or grows with element-set age in the model of rule 3 (section 6).

### 2.3 Evidence model

An orbit solution is an inference with five contributors:

1. The object's physics
2. The dynamics model
3. The observations
4. The measurement model
5. The inference method

A provider's ephemeris or element set already embeds the provider's models and inference, so the catalog treats it as the provider's estimate, not as an observation. Agreement between two products counts as independent confirmation only when their inputs are independent.

Orbit data sit at four processing levels, by analogy with Earth-science data levels. A higher level is more processed, not better: it inherits every flaw below it, and re-epoching adds no evidence. Each input is placed at its level.

| Level | Content | SDN inputs |
| --- | --- | --- |
| 0 | Raw sensor data | Detector counts, signal samples, images |
| 1 | Calibrated, time-tagged measurements | Laser normal points; radio Doppler; optical angles |
| 2 | Estimated states and ephemerides | Operator ephemerides; GNSS and laser-ranging precise orbits; Vimpel elements; ILRS predictions |
| 3 | Theory-specific mean elements fitted to level 1 or 2 | OMMs from Space-Track and SpaceMapper; CelesTrak's supplemental GP; SDN's SGP4 companions |

A solution states what produced it:

| Field | Content |
| --- | --- |
| State and validity | State or ephemeris, epoch, applicable interval |
| Inputs | The products it rests on and their levels |
| Dynamics | Force model, environment inputs, parameters estimated, considered or assumed |
| Measurement model | Sensor geometry, timing, calibration, biases; unknown when a provider does not state it |
| Inference | Estimator or selection rule, weights, priors |
| Uncertainty | Covariance, its source and its calibration label |

Physical validity rests on the measurements of sections 5 to 7.

## 3 Sources

The inventory<sup>[2](#r2)</sup> lists 97 public and login-only sources in eight groups, with access, terms, latency and use. Results draw on every source an experiment read; files are copied only where their terms allow, and the credits close this paper.

| Group | Sources | Role |
| --- | --- | --- |
| A. Precise orbits | IGS and ESA GNSS orbits; ILRS orbits and normal points; Sentinel-1, Swarm, GRACE-FO and CryoSat-2 precise orbits | Truth; the GNSS and laser-ranged products |
| B. Operator ephemerides | SpaceX Starlink (with covariance), Planet, NASA ISS, CMSE Tiangong, Intelsat; ILRS predictions | Operator products; drag and maneuver evidence |
| C. Catalogs and observations | Space-Track GP history (OMM) and SATCAT; JSC Vimpel; SpaceMapper AOE; SatNOGS Doppler | Element sets; other providers' catalogs; public observations |
| D. Density | Swarm and GRACE-FO densities; CHAMP and GRACE-A densities with the High Accuracy Satellite Drag Model (HASDM)<sup>[3](#r3)</sup> | Density truth; the HASDM comparison |
| E. Space weather | Space Environment Technologies' (SET) JB2008 indices; GFZ Kp, ap and F10.7; NOAA SWPC forecasts | Model drivers, as issued |
| F. Earth orientation, gravity, ephemerides | IERS EOP; EGM2008, EGM96; FES2004; DE440 | Frames and dynamics |
| G. Physical properties | GCAT; IGS satellite metadata | Priors for drag and radiation parameters |
| H. The High Accuracy Catalog (HAC) and HASDM | Special Perturbations (SP) vectors; real-time HASDM | The comparison target (section 10) |

SpaceMapper's AOE catalog holds SGP4 element sets from its own ground and space sensors (channel KY, 19,202 objects on 11 October 2026) beside sets it relays from other catalogs<sup>[4](#r4)</sup>. AOE rewrites them every 3 hours; SDN reads them daily from the public web pages (section 4). SpaceMapper states no license, so the sets are cited, not copied. Doppler read from SatNOGS waterfall images made the ISS orbit 2.7–10.7 times worse than SGP4 [E6][e6].

## 4 Normalization and model dispatch

### Vimpel osculating elements

Vimpel's HEO product gives osculating elements in J2000<sup>[5](#r5)</sup>. The standard perifocal rotation turns them into a J2000 state at the same epoch<sup>[6](#r6)</sup>, with μ = 398,600.4418 km³/s². Vimpel's datefirst file maps its own object numbers to NORAD numbers, with the date each catalog first detected the object; each pair is a candidate link, accepted when the two orbits agree (section 8). An object the file does not map keeps the identifier `vimpel:<id>`, and no NORAD number is invented<sup>[7](#r7)</sup>. Velocity differenced from 600 s position samples departs from the analytic velocity by up to 1.1 km/s, so it serves only as a diagnostic.

### AOE element sets

AOE's KY sets are SGP4 mean elements: the catalog evaluates them through the same SGP4 as Space-Track's OMMs, under the NORAD number AOE gives, or under `aoe:<id>` where it gives none. E15 scores them against independent orbits beside Space-Track's, under a plan fixed before its test window [E15][e15]. On 11 October 2026, its first training day:

| Measure | Result |
| --- | --- |
| Difference from Space-Track | A median 1.70 km from Space-Track's newest set at the same moment; 1.5 % within 100 m |
| Age at issue | Median 11.6 days: 7.4 in Starlink's shells, 36 for the Chinese space station, 62 for Spire, 83 for Planet |
| Error at issue, Starlink (2,755 satellites, SpaceX ephemerides) | 40.7 km median against Space-Track's 2.14 km: 18.9 [16.7, 21.6] times, almost all set age |
| The same at matched age | 1.07 [0.70, 1.55] times Space-Track's error under half a day old; 1.03 [0.80, 1.26] at half a day to a day |
| Error at issue, GEO (76 satellites) | 19.0 km against 6.69 km: 2.84 [1.77, 4.07] times |
| Coverage | 45 of 82 Planet and 6 of 10 Spire satellites have no AOE set |

AOE's sets are therefore not copies of Space-Track's: they are as accurate at the same age, and older on most objects.

### The handoff contract

An OMM holds SGP4 mean elements, not osculating ones. The catalog evaluates SGP4 at zero elapsed time, transforms position and velocity from TEME to GCRF at the same instant, and starts HPOP from that state<sup>[8](#r8)</sup>. The rotation uses the IAU 2006/2000A precession-nutation and the equation of the equinoxes. Against the GMST82 and IAU 1976/1980 rotations of Vallado et al.<sup>[8](#r8)</sup>, this convention moves positions by 2.0–2.6 m in LEO and 12–16 m at GEO; the catalog uses it throughout. SGP4's drag term B* becomes the ballistic parameter

$$
B = \frac{C_D A}{m} = \frac{2B^*}{\rho_0}
$$

with ρ₀ from Spacetrack Report No. 3<sup>[9](#r9)</sup>; 0.01 m²/kg stands in where B* ≤ 0, and Cr·A/m is 0.02 m²/kg<sup>[10](#r10)</sup>. The handoff is valid; whether it helps is measured in section 5.3.

### Levels of measurement of OMM elements

OMM fields mix nominal (catalog number), ordinal (element-set number), interval (epoch), ratio (mean motion) and circular (node, perigee, mean anomaly) scales, so the pipeline computes on SGP4-evaluated states, never on raw fields. The legacy two-line format quantizes angles to 0.0001°, about 12 m along track near 6,900 km: a resolution, not an accuracy.

### Dispatch, frames and time

Each record is routed by its declared theory: mean elements to SGP4, osculating elements to the two-body conversion, states and ephemerides to frame transforms. Osculating elements offered to SGP4 are refused as a model mismatch. Frames follow the IERS Conventions (2010): IAU 2006/2000A, CIO-based, through ERFA, with IERS Earth orientation<sup>[11](#r11)</sup>. TEME, EME2000, GCRF and ITRF stay distinct; epochs are UTC and integration runs on TT.

### SDN's own OMMs

SDN fits its own OMMs to any source's ephemeris: operator ephemerides (SpaceX's MEME files, CCSDS OEM, Earth-fixed files), GNSS orbits (SP3), laser-ranging predictions (CPF) and its own HPOP solutions. Each fit gives two element sets over a declared interval, each scored on a held-out stretch before release: an SGP4 set with B*, which any SGP4 implementation reads, and an SGP4-XP set with its ballistic (BTERM) and radiation (AGOM) coefficients, propagated by the Astrodynamics Standards library<sup>[52](#r52)</sup>. CelesTrak's supplemental GP fits operator ephemerides with SGP4 the same way<sup>[48](#r48)</sup>, Spacebook fits extended-perturbation element sets to its own orbits<sup>[51](#r51)</sup>, and the 18th Space Defense Squadron's eGP fits SP states<sup>[49](#r49)</sup>. The fit RMS that CelesTrak publishes for each set is the benchmark.

Exploratory, on 24-hour fits to the same states [XP][xp]: the median root-mean-square 3D position error (km) over the fit and over each later day, SGP4-XP / SGP4.

| Ephemeris (windows) | Fit | Day 1 | Day 3 | Day 7 |
| --- | ---: | ---: | ---: | ---: |
| GPS, Galileo and GLONASS, ESA final orbits (98) | 0.022 / 0.134 | 0.057 / 0.420 | 0.161 / 2.40 | 0.419 / 14.2 |
| LAGEOS-1/2 and Etalon-1/2, ILRS orbits (36) | 0.095 / 0.116 | 0.104 / 0.227 | 0.242 / 1.22 | 0.570 / 6.61 |
| Seven low laser-ranged satellites, ILRS orbits (139) | 0.371 / 0.389 | 0.735 / 0.977 | 1.16 / 5.10 | |
| Starlink, SpaceX ephemerides (196) | 0.539 / 0.534 | 3.67 / 5.92 | | |
| ISS, NASA ephemeris (14) | 0.455 / 0.461 | 2.16 / 2.12 | 16.5 / 16.4 | 88.9 / 88.8 |

Starlink files span 2–3 days and the low laser-ranged arcs 4 days, which bounds their horizons. SGP4-XP converged on all 505 windows and SGP4 on 98 of the 120 GNSS windows; the GNSS row compares those 98. Where drag is weak the extended theory holds: 12 to 34 times closer at 7 days on GNSS, LAGEOS and Etalon. On the ISS, where drag and reboosts dominate, the two theories are equal.

## 5 Products by object class

Each row is one method: its originators, our measured result (median 3D position error unless stated) and the experiment. Every experiment froze its plan before reading its test window<sup>[1](#r1)</sup>.

### 5.1 GNSS and laser-ranged satellites

| Method | Prior art | Measured | Evidence |
| --- | --- | --- | --- |
| Newest ultra-rapid analysis-centre orbit at issue, propagated numerically beyond | IGS products<sup>[12](#r12)</sup> | GPS, 32 satellites: 3.5 cm at issue, 336 m at 7 days; the latest public OMM 1.5–1.7 km | [E3][e3] |
| HPOP fitted to ILRS laser normal points | Laser-ranging orbit determination<sup>[13](#r13)</sup> | Starlette, Stella, LARETS, WESTPAC, LARES: 7 m at the fit and 42 m at 7 days, against SGP4's 472 m and 685 m. One pass beats the OMM for a day; four passes beat it at every horizon to 7 days | [E6][e6] |

### 5.2 Operator- and provider-ephemeris objects

| Method | Prior art | Measured | Evidence |
| --- | --- | --- | --- |
| Operator ephemeris, within its span | Operator orbit determination | GNSS analysis centres' predictions (IGS for GPS, ESA for GLONASS): 0.1 m from final orbits at 0–12 h | [E4][e4] |
| SGP4 on the latest public OMM, against operator ephemerides | Dyreby et al. 2025<sup>[14](#r14)</sup>; Jankovic 2026<sup>[15](#r15)</sup> | At the file's start: Planet 0.59 km, ISS 0.82 km, GPS 1.23 km, Starlink 2.87 km, Intelsat 4.79 km; Starlink 13.8 km at 24 h | [E4][e4] |
| Vimpel's state refined to Vimpel's ephemeris by one six-state correction | Weighted least squares<sup>[16](#r16)</sup> | Withheld RMS 6.04–10.71 km before, 52.8–69.9 m after, on three 4-hour arcs: consistency with one provider, not accuracy | [R7](#r7)7

### 5.3 Objects with public OMMs only

| Method | Prior art | Measured | Evidence |
| --- | --- | --- | --- |
| SGP4 on the latest public OMM | Hoots and Roehrich 1980<sup>[9](#r9)</sup>; Vallado et al. 2006<sup>[8](#r8)</sup> | At issue: 0.31 km (laser-ranged LEO) to 1.57 km (GPS); by horizon in section 10 | [E3][e3] |
| Along-track correction at epoch (GPS) | Ly et al. 2020: 65 %<sup>[17](#r17)</sup>; Hallgarten La Casta and Amato 2025<sup>[18](#r18)</sup> | RMS at epoch 2.04 → 0.79 km (61 % [53, 67]); the single-model form 8 %; worse than SGP4 by 7 days (2.55 against 1.88 km); its covariance fails the coverage gate. On an orbital plane outside the fit: 6 % [2, 10] | [E1][e1] |
| The epoch correction learned on other objects, by regime (LEO: static covariates and a 48-hour fleet nowcast; MEO/GEO: the form of Ly et al.) | Ly et al. 2020<sup>[17](#r17)</sup>; Hallgarten La Casta and Amato 2025<sup>[18](#r18)</sup> | Unseen LEO satellites: 25 % [18, 32] lower error at epoch, 51 % for Planet's own satellites; other operators' satellites −2 % [−17, 10]; 9 % at 1 day, 1 % at 3 days. MEO and GEO 4 %, worse by more than 10 % for 64 of 135 objects. A per-satellite bias holds 37 % of the along-track variance in GNSS, 1–5 % in LEO | [E16][e16] |
| HPOP from the OMM's epoch state | Vallado et al. 2006<sup>[8](#r8)</sup>; Jankovic 2026<sup>[15](#r15)</sup> | Equal to SGP4 at epoch, then larger as the OMM's start-state error grows: at 24 h, 1.7 (GPS), 9.5 (LEO with precise orbits) and 11 (laser-ranged LEO) times SGP4's error; larger in all four regimes (GPS, LEO with precise orbits, laser-ranged LEO and MEO) by 72 h | [E7][e7] |
| AOE's newest set | SpaceMapper<sup>[4](#r4)</sup> | Starlink: 18.9 times Space-Track's error at issue, from set age; 1.03–1.07 times at matched age (section 4) | [E15][e15] |
| HPOP fitted to an arc of OMM epoch states | Levit and Marshall 2011<sup>[19](#r19)</sup> | Better than SGP4 only at short horizons: 0.32–0.51 of its error to 12 h (laser-ranged LEO), 0.36–0.53 to 48 h on E1-corrected GPS sets; undecided beyond | [E7][e7] |
| HPOP fitted to a 4- or 7-day arc of OMM epoch states with drag fitted (LEO) | Levit and Marshall 2011<sup>[19](#r19)</sup> | 0.45 of SGP4's error at epoch and 0.39 at 6 h; 0.87 [0.62, 1.18] at 24 h and 1.40 [1.00, 1.92] at 72 h (not supported); laser-ranged spheres 0.28 at 24 h and 0.46 at 72 h. A fitted drag rate, weights with the measured correlation and the E13 density correction leave it unchanged | [E16][e16] |

The start state sets the limit. Over 6 days, HPOP leaves 0.2 km along track from a GPS satellite's IGS state and 8.4 km from its OMM epoch state (medians of 508 arcs)<sup>[10](#r10)</sup>. Laser ranges fix the start state, and the prediction then stays 20 to 60 times better than SGP4 to 7 days [E6][e6]. In LEO, drag limits even a precise start: from the true state, with Cd·A/m from B* (0.01 m²/kg where B* ≤ 0), HPOP's median error is 481 m at 24 h and 4.5 km at 72 h [E7][e7].

### 5.4 Drag regime

Density is scored by σ, the spread of ln(observed/model density) on satellites outside the calibration; prediction by the 3-day position error relative to JB2008's (below 1 is better).

| Method | Prior art | Measured | Evidence |
| --- | --- | --- | --- |
| JB2008 with SET's indices | Bowman et al. 2008<sup>[20](#r20)</sup> | σ 0.155 (Swarm, GRACE-FO, 2026); 0.148 on CHAMP and GRACE-A in 2003 and 2008, where HASDM scores 0.078 | [E5][e5] |
| Temperature correction from accelerometer and precise-orbit densities, a stand-in for HASDM's calibration | Storz et al. 2005<sup>[21](#r21)</sup>; Doornbos 2012<sup>[22](#r22)</sup> | σ 0.056, 64 % [57, 70] lower; 0.112 on HASDM's windows; forecast gain 9 % for one day; 3-day ratio 1.04 [0.96, 1.10]; inputs arrive 49–92 days late ([E8][e8]) | [E5][e5] |
| Temperature correction from the OMM decay of 48 catalogued objects | Picone et al. 2005<sup>[23](#r23)</sup>; Gondelach and Linares 2020<sup>[24](#r24)</sup>; Gershman 2026<sup>[25](#r25)</sup> | σ 0.142, 8.4 % [−3.9, 18.3] lower (25 % required: not supported); 24.7 % lower in storms; as a forecast 16.8 % worse than JB2008; 3-day ratio 1.02; 0.270 on HASDM's windows | [E8][e8] |

**An open, DCA-form correction from public operator states** [E13][e13]. HASDM's Dynamic Calibration of the Atmosphere (DCA) corrects exospheric temperature every 3 hours from the drag of about 75–100 tracked calibration satellites, each with a constrained ballistic-coefficient correction<sup>[21](#r21)</sup>. E13 solves that form from public data alone: the common mode s of the daily drag of 36 Planet Doves, read through orbital energy, corrects JB2008's temperature, with a ballistic coefficient held constant per satellite over a block and a forecast with a 48-hour time constant. Published work derives density from operator ephemerides and scores density alone<sup>[26](#r26),[27](#r27),[28](#r28)</sup>, or assimilates commercial GPS states<sup>[29](#r29)</sup>. E13 scores the correction on satellites that did not calibrate it, over ten months of Planet states against Swarm and GRACE-FO, with the storm of January 2026 in a held-out block. With x′ the logarithmic residual of observed against JB2008 density in a 12-hour window,

$$
R_1 = 1 - \frac{\operatorname{sd}(x' - s)}{\operatorname{sd}(x')}
$$

| Pre-registered test (threshold) | Test A, Jan–Mar 2026, Kp 8.7 storm | Test B, Apr–Jul 2026 |
| --- | --- | --- |
| H1: Swarm and GRACE-FO density spread explained, R₁ (≥ 0.25) | 0.247 [0.137, 0.347]: not supported | 0.401 [0.283, 0.501]: supported |
| H3: held-out Doves' drag explained (≥ 0.2) | 0.378 [0.223, 0.477]: supported | 0.254 [0.135, 0.359]: supported |
| H4: Swarm position error ratio at 1 and 3 days (≤ 0.9) | 1.02, 0.92: not supported | 1.01, 1.00: not supported |
| H5: held-out Doves, the same | 0.98, 0.97: not supported | 1.00, 1.01: not supported |
| H6: control, correction shifted 7 days (R₁ < 0.05) | −0.149: passes | −0.082: passes |

The correction does not follow storms. Exploratory, on one NRLMSISE-00 reference for every split: the fleet explains 0.47–0.48 of NRLMSISE-00's larger spread, leaving residuals of 0.075–0.095 against 0.069–0.086 on JB2008.

### 5.5 Maneuver detection

The detector compares the trajectories of consecutive element sets at one instant, whatever propagator answers its queries: a crossing test and a robust likelihood-ratio test for steps in orbit-averaged energy and orbit plane, merged by default<sup>[10](#r10)</sup>. It builds on element-set time series<sup>[30](#r30)</sup> and the generalized likelihood ratio for jumps<sup>[31](#r31)</sup>. Thresholds came from the ISS in 2020–2024; every other window was read once. A planned ISS burn counts as truth only when NASA's next trajectory shows it executed; one 2021 plan was not.

| Held-out class, 2025–2026 | Truth | Found | False alarms |
| --- | --- | --- | --- |
| ISS | 25 burns | 22; the misses are 0.1 m/s settling burns minutes before reboosts; median time error 10 min, Δv ratio 1.00 | 0.57 per year |
| Metop-SG A1 | 24 EUMETSAT maneuvers | 14; 12 of 16 at 0.1 m/s or more | 0 |
| GPS, 4 satellites | 6 maneuvers in precise orbits | 6 | 3.8 per object-year |
| GEO (Intelsat) | 11 post-maneuver files | 4 | 24 events per object-year; the truth lists one maneuver per satellite |

## 6 Uncertainty

Covariance propagates through HPOP's state-transition matrix Φ with a declared white-acceleration process noise Q<sup>[10](#r10)</sup>:

$$
P(t) = \Phi(t, t_0)\, P_0\, \Phi(t, t_0)^{\mathsf{T}} + Q(t)
$$

Φ supplies neither P₀ nor Q; they come from a fit, a provider or an empirical model. Realism is scored against independent precise orbits by $d^2 = \mathbf{e}^{\mathsf{T}} P^{-1} \mathbf{e}$ for a position error e, against χ² with 3 degrees of freedom: a covariance is realistic when the mean of d²/3 lies in [0.8, 1.25] and its 95 % coverage in [0.93, 0.97], the rule of [E14][e14] after Zaidi and Hejduk<sup>[32](#r32)</sup>.

| Covariance | Prior art | Held-out result | Evidence |
| --- | --- | --- | --- |
| Formal covariance of an HPOP fit to laser ranges | Weighted least squares<sup>[16](#r16)</sup> | Too small: mean d²/3 16–21, coverage 0.14–0.18 | [E6][e6] |
| HPOP fit to OMM history alone | Levit and Marshall 2011<sup>[19](#r19)</sup> | Realistic for laser-ranged LEO (0.68–0.83, coverage 0.96–0.98); too small for the ISS within a day (4.8–7.6) | [E6][e6] |
| Batch fit to 3 days of OMM history (GPS) | Osweiler 2006<sup>[33](#r33)</sup>; Geul et al. 2017<sup>[34](#r34)</sup> | Mean d²/3 1.23 and 1.05 at 0 and 1 day, 81 at 3 days; consistency failed at every horizon | [E2][e2] |
| Errors of consecutive OMMs, against precise orbits | Autocorrelation shown by Osweiler<sup>[33](#r33)</sup> | Along-track correlation 0.61–1.00 in four regimes. Four published constructions<sup>[33](#r33),[35](#r35),[34](#r34),[36](#r36)</sup> are overconfident: mean d²/3 from 13 to over 10⁷. A covariance that models the correlation scores better at 1 day in three regimes but is consistent only for laser-ranged satellites at some horizons | [E2b][e2b] |
| SGP4 with the E2b error model, calibrated by regime and age | Zaidi and Hejduk 2016<sup>[32](#r32)</sup> | Realistic in 2 of 35 regime-horizon cells (11 on the selection window) | [E14][e14] |
| HPOP from the OMM epoch state, with P₀ and Q per regime | SP covariance as the Astrodynamics Support Workstation (ASW) forms it<sup>[37](#r37)</sup> | 5 of 35 (6 on the selection window): 500–800 km at epoch, above 800 km to 2 days | [E14][e14] |
| Density-forecast consider term, in the form of the Dynamic Consider Parameter | Hejduk 2019<sup>[38](#r38)</sup> | No measurable effect in storms; density-error coverage 0.83–0.86 against 0.95 | [E14][e14] |
| Covariance of the corrected epoch state, unseen LEO objects | Ly et al. 2020<sup>[17](#r17)</sup> | Mean d²/3 1.01 and coverage 0.949, inside the rule; not supported once corrected for multiple tests | [E16][e16] |

On the held-out window, maneuvers, the storm of 4 July 2026 and GNSS repositioning put heavy tails on the errors: mean d²/3 rises while medians and coverage stay near nominal in LEO, so realism found on the selection window did not carry over. Operator covariance enters as supplied, labeled with its source.

## 7 Dynamics and verification

HPOP's full force model, as the catalog and the conjunction screen run it<sup>[10](#r10)</sup>, beside the SP options a Vector Covariance Message (VCM) names<sup>[39](#r39)</sup>:

| Model | HPOP | SP, as a VCM names it | Equivalence (Orekit: steps of at most 30 s) |
| --- | --- | --- | --- |
| Geopotential | EGM2008 to degree and order 70; EGM96 and tesseral truncation (36Z,24T) selectable | EGM-96 with zonal and tesseral degrees | EGM96 cases within 1.1 mm of Orekit |
| Atmosphere | NRLMSISE-00 (Orekit's to 1e-8); JB2008 (Orekit's to 2e-14); Jacchia-Roberts (GMAT's<sup>[40](#r40)</sup>, bit for bit) | JBH09, which is JB2008 with HASDM's calibration<sup>[41](#r41)</sup>; JAC70; MSIS90 | JB2008 matches Orekit's; HASDM's calibration is not public (section 5.4); Jacchia-Roberts and NRLMSISE-00 are declared substitutes for JAC70 and MSIS90 |
| Third bodies | Sun, Moon, Venus, Mars and Jupiter from DE440s | Sun and Moon | Sun and Moon within 1 cm of Orekit |
| Radiation | Cannonball with a conical shadow; Earth albedo and infrared | Solar radiation pressure, AGOM | Within 1 cm of Orekit |
| Tides, relativity | IERS 2010 solid tides and relativity; FES2004 ocean tides to 30×30 | Solid Earth tides | Within 1.3 mm of Orekit |
| Drag parameter, thrust | Cd·A/m and its rate; in-track acceleration | B, BDOT; in-track thrust | Within 3.8 mm of Orekit |
| Integrator | RK78 at 1e-12, steps of at most 300 s; its own error after 3 days 0.1–1.2 m | Eighth-order Gauss–Jackson on Cowell's equations<sup>[42](#r42),[43](#r43)</sup> | Declared substitute, converged |
| Earth orientation | IAU 2006/2000A with daily IERS rows | IAU 1980 nutation, truncated | Declared substitute |

### Agreement with Orekit

HPOP's Orekit 13.1 suite<sup>[44](#r44)</sup> holds 72 cases on five orbits (400 km, 700 km sun-synchronous, GPS, geostationary, Molniya), each 24 hours, compared hourly on identical states, constants, daily Earth-orientation rows and space weather<sup>[10](#r10)</sup>. HPOP 1.3.0 (module SHA-256 9c24bf0e) integrates with RK78 at a relative tolerance of 1e-13 (point mass at 1e-15) and the steps named below; Orekit with Dormand–Prince 8(5,3) at 1e-14 and steps of at most 10 s:

| Force set | Steps of at most 300 s | Steps of at most 30 s |
| --- | ---: | ---: |
| Point mass | 0.6 mm | 0.6 mm |
| Gravity field, third bodies, radiation pressure, tides, relativity, in-track thrust | 1.9 cm | 9.9 mm |
| With drag (NRLMSISE-00 or JB2008, daily space weather, drag rate) | 2.5 cm | 4.2 mm |

At steps of up to 300 s, HPOP's own integration error is most of the difference: 1.8 cm over a day at 400 km (2.6 cm with JB2008 drag) and 1.6 cm at 700 km, against 0.03 mm for GPS satellites. With steps of 30 s the rows no longer depend on the step (a tolerance of 1e-15 with steps of 10 s moves them by under 0.2 mm), and the largest difference is in Molniya, 9.9 mm; 400 km and 700 km agree to 4.2 mm with drag and 1.7 mm without, and GPS and geostationary orbits to 0.2 mm. The rest is Earth orientation: Orekit applies the IERS Conventions' tidal and libration corrections to polar motion and UT1<sup>[11](#r11)</sup>, and HPOP interpolates the daily rows. Against Orekit without those corrections the differences at 30 s are 0.6 mm for point mass, 1.1 mm without drag and 3.9 mm with it, and 0.6 mm in Molniya.

State-transition matrices agree within 2e-5 and parameter sensitivities within 1.5e-4 (relative), at steps of up to 300 s. Integrating Jacchia's diffusion equations numerically reproduces the closed forms of Jacchia-Roberts<sup>[45](#r45)</sup> to 1.6e-4.

### Accuracy against precise orbits

Experiment V1 starts the propagator from ILRS orbits of four geodetic spheres (LAGEOS-1/2 and ETALON-1/2; 16 seeds) and IGS orbits of 32 GPS satellites (96 seeds), adds forces one set at a time, and compares each 72-hour run with the same product [V1][v1]. GPS radiation pressure is a deliberately crude cannonball. Neither class feels significant drag; in LEO, drag from B* limits a precise start (section 5.3). Median (largest) 3D error:

| Force set | Spheres, 24 h | Spheres, 72 h | GPS, 24 h | GPS, 72 h |
| --- | ---: | ---: | ---: | ---: |
| E-a: point mass | 46 km (396 km) | 110 km (1,213 km) | 25 km (40 km) | 75 km (170 km) |
| E-b: zonal harmonics to degree 20 | 1.3 km (2.2 km) | 3.4 km (8.7 km) | 2.1 km (6.5 km) | 6.0 km (94 km) |
| E-c: E-b with the Sun and Moon | 257 m (2.1 km) | 677 m (6.2 km) | 331 m (868 m) | 983 m (89 km) |
| E-d: E-c with radiation pressure | 246 m (2.1 km) | 670 m (6.2 km) | 277 m (661 m) | 853 m (89 km) |
| E-e: E-d with the full 20×20 field and IERS Earth orientation | 6.2 m (15 m) | 18.6 m (51 m) | 29.5 m (117 m) | 92 m (90 km) |
| E-f: E-e with IERS 2010 solid tides and relativity | 4.8 m (10 m) | 13.9 m (31 m) | 28.5 m (117 m) | 91 m (90 km) |

The GPS maxima beyond a day are one satellite, NORAD 35752, in every force set. Of four pre-registered checks, two passed and two failed:

1. Passed: the two-body return, 0.8 mm.
2. Passed: the E-d bound at 24 hours, 246 m against 500 m.
3. Failed: an added force did not reduce every seed's error (14 violations, all without tesseral harmonics).
4. Failed: a 20×20 field without the Sun and Moon beat zonals alone for only 53 of 96 GPS seeds.

HPOP's resident path, which holds the catalog in memory, reproduces its execution path to 0.052 mm over four days, at 0.645 s per object-day on one thread<sup>[10](#r10)</sup>.

## 8 Association and selection

Association asks whether records describe one object; selection picks a solution for an epoch, interval and use; estimation derives a new one. Candidates come from provider crosswalks, such as Vimpel's datefirst file of Vimpel and NORAD numbers, international designators and native-identifier histories; names and bare numbers are not join keys. Compatibility is tested on normalized ephemerides over shared intervals. Each candidate link is compatible, rejected, ambiguous or insufficient, and accepted links stay reversible. Selection takes, per regime and horizon, the source with the lowest median error on a training window of at least 30 samples [E3][e3].

| Rule | Measured | Evidence |
| --- | --- | --- |
| Selection by evidence | GPS: 3.5 cm at issue (section 5.1) | [E3][e3] |
| Fusion of sources | Never better than selection: 1.07–1.17 times its error in GPS at 1–7 days, equal elsewhere | [E3][e3] |
| Covariance intersection<sup>[46](#r46)</sup> of one provider's consecutive OMMs | Collapses onto one set (0.90–1.00 of selection's error); naive fusion is overconfident (mean d²/3 3.3–700) | [E2b][e2b] |

## 9 SP message interchange

The VCM carries an SP state, its force settings and an equinoctial covariance<sup>[39](#r39)</sup>. SDN's adapter reads a VCM into an HPOP request, transforming the covariance from equinoctial to Cartesian with an exact Jacobian computed by forward-mode dual numbers<sup>[47](#r47)</sup>. The format states no covariance units, and no public definition was found. Four messages settle them:

1. The survey's ISS sample
2. An SP message for a geostationary orbit
3. An SP message for a GPS orbit
4. An SP message for an eccentric (e = 0.59) orbit

| Covariance rows | Reading | Evidence |
| --- | --- | --- |
| Mean motion | Relative, dn/n; covariance scaled by max(1, weighted RMS)² | Every printed U, V and W sigma reproduced within 1 %; no absolute unit does (radians per 1000 s misses two radial sigmas by 1.7 and 2.0 times) |
| B and AGOM | Fractions of the coefficients (1.5–9.7 %) | The GPS message against ESA's final orbit for one day: radial and in-track RMS 0.46 and 0.64 of their sigmas, cross-track 1.27; read as printed, the sigmas overstate the error 7 to 20 times |

A VCM naming JBH09 runs as JB2008 on SET's indices, which the caller supplies.

## 10 Measured accuracy

Median 3D position error against precise orbits, by object class and age of the product:

| Class (objects) | Product | At issue | 1 day | 3 days | 7 days | Evidence |
| --- | --- | ---: | ---: | ---: | ---: | --- |
| GPS (32) | Ultra-rapid orbit, propagated | 3.5 cm | 30 m | 104 m | 336 m | [E3][e3] |
| GPS (32) | Public OMM, SGP4 | 1.57 km | 1.61 km | 1.68 km | 1.53 km | [E3][e3] |
| Laser-ranged LEO (5) | HPOP fitted to laser ranges | 7 m | 9 m | 17 m | 42 m | [E6][e6] |
| Laser-ranged LEO (5) | Public OMM, SGP4 | 472 m | 485 m | 482 m | 685 m | [E6][e6] |
| Laser-ranged LEO, 800–1,500 km (5) | Public OMM, SGP4 | 314 m | 326 m | 326 m | 569 m | [E3][e3] |
| Laser-ranged MEO (5) | Public OMM, SGP4 | 411 m | 438 m | 395 m | 413 m | [E3][e3] |
| LEO, 400–800 km (6) | Public OMM, SGP4 | 566 m | 597 m | 760 m | 1.24 km | [E3][e3] |

The HAC's position errors are not published<sup>[2](#r2)</sup>. The public figures: element sets in the HAC's own LEO conjunction regime (perigee above 500 km) err by 0.6–3 km from epoch to 72 hours<sup>[49](#r49)</sup>; one ASW covariance shown as an illustration has σ of 8.3 m radial, 332 m in-track and 2.1 m cross-track at epoch<sup>[37](#r37)</sup>; HASDM's density spread is 0.078 against JB2008's 0.148 (section 5.4).

## 11 What conjunction assessment consumes

Conjunction screening<sup>[50](#r50)</sup> takes from one catalog snapshot, per object:

| Input | From the catalog |
| --- | --- |
| Trajectory | The OMM for SGP4, or one propagator's trajectory; one propagator per run |
| Covariance | Supplied by the operator, fitted, or modeled (section 6), with its calibration label |
| Hard-body radius | Supplied, half the catalog size, from radar cross-section, or the default, with its basis |

An OMM supplies no covariance, so the catalog attaches one by the rule of section 2.2: built from the source's element-set errors against independent orbits and calibrated by regime and age, with its calibration label. Every event also carries the Alfano maximum, the largest probability a spherical covariance of any size gives, which needs no covariance. Products built from public OMMs are advisory: NASA's conjunction assessment handbook (Vol. 2, App. E) and the Space-Track user agreement state that public element sets alone must not drive conjunction assessment decisions<sup>[49](#r49),[53](#r53)</sup>.

## Appendix A. Terms

| Term | Meaning |
| --- | --- |
| ASO | Anthropogenic space object |
| OMM, GP | CCSDS Orbit Mean-elements Message: SGP4 mean elements, also distributed in the legacy two-line format; general perturbations element set |
| SGP4, B* | The analytic propagator of OMMs; its drag term |
| HPOP | High-precision orbit propagation: numerical integration of the full force model of section 7 |
| Provider estimate | A provider's product, which already embeds its models and inference |
| Level 0 to 3 | Processing levels of orbit data (section 2) |
| Ultra-rapid, final orbits | GNSS analysis-centre orbits issued in near real time with a prediction; definitive orbits issued about two weeks later |
| SLR, ILRS | Satellite laser ranging; the International Laser Ranging Service |
| TEME, EME2000, GCRF, ITRF | SGP4's output frame; the J2000 mean equator and equinox; the Geocentric Celestial Reference Frame; the terrestrial frame |
| d², mean d²/3 | Squared Mahalanobis distance of an error under its covariance; the mean of d²/3 is 1 for a realistic covariance |
| Held-out data | Data set aside while a method is chosen or tuned and used only to score it: a later time window, or objects the tuning never saw (section 1) |
| Selection window, training window | The data a method is chosen or tuned on; its result is reported on held-out data (section 1) |
| DCA, DCP | HASDM's Dynamic Calibration of the Atmosphere; the Dynamic Consider Parameter for density-forecast error |
| HASDM, JB2008, JBH09 | High Accuracy Satellite Drag Model; the Jacchia-Bowman 2008 density model; JB2008 with HASDM's calibration |
| SP, HAC, ASW | Special Perturbations orbit determination and propagation; the High Accuracy Catalog; the Astrodynamics Support Workstation |
| VCM | SP Vector Covariance Message |
| Supplemental GP, eGP | CelesTrak's element sets fitted to operator ephemerides; the 18th Space Defense Squadron's element sets fitted to SP states |
| AOE | SpaceMapper's catalog of element sets from its own sensors |
| Covariance intersection | A fusion rule that stays consistent under unknown correlation |

## Data credits

- IGS products, courtesy of the International GNSS Service and its analysis and data centres (Johnston, Riddell and Hausler 2017, doi:10.1007/978-3-319-42928-1). © Navigation Support Office at ESA/ESOC 2026.
- GFZ rapid MGEX orbits of the IGS Multi-GNSS Experiment (Montenbruck et al. 2017, doi:10.1016/j.asr.2017.01.011), obtained from NASA CDDIS (Noll 2010, doi:10.1016/j.asr.2010.01.018).
- ILRS data and products (Pearlman et al. 2019, doi:10.1007/s00190-019-01241-1), from the EUROLAS Data Center, DGFI-TUM (Noll et al. 2019, doi:10.1007/s00190-018-1207-2). NERC Space Geodesy Facility orbits; contains British Geological Survey materials © UKRI 2026, Open Government Licence v3.0.
- Contains modified Copernicus Sentinel data 2026.
- © ESA 2026; data provided by the European Space Agency: SW_SP3xCOM_2_ (doi:10.57780/esa-8579355), SW_DNSxPOD_2_ (doi:10.57780/esa-ef59315), GF_DNSxACC_2_ (doi:10.57780/esa-2559fd1).
- Licata, Mehta, Tobiska, Bowman and Pilinski (2021), CHAMP and GRACE-A density estimates with associated HASDM and JB2008 predictions, Zenodo, doi:10.5281/zenodo.4602380, CC BY 4.0.
- Source: USSPACECOM / 18th Space Defense Squadron, via Space-Track.org.
- SatNOGS Network observations (Libre Space Foundation; the station and observer named in each record), https://network.satnogs.org/, CC BY-SA 4.0.
- SpaceX Starlink public ephemerides, https://api.starlink.com/public-files/ephemerides/ (SpaceX). Planet Labs PBC orbital ephemerides, https://ephemerides.planet-labs.com/, © Planet Labs PBC, CC BY-NC 4.0. ISS trajectory: NASA JSC Flight Operations Directorate (U.S. Government work). China Manned Space Engineering Office, https://www.cmse.gov.cn/. Intelsat (SES) public ephemerides, https://my.intelsat.com/ephemeris/public. EUMETSAT.
- JSC Vimpel, http://spacedata.vimpel.ru/. SpaceMapper, https://spacemapper.cn/. CelesTrak, https://celestrak.org (T. S. Kelso). Data from J. McDowell, planet4589.org.
- The JB2008 solar and geomagnetic indices are provided for scientific use courtesy of Space Environment Technologies (https://spacewx.com/jb2008/). Kp, ap, Ap: GFZ Helmholtz Centre for Geosciences, CC BY 4.0 (Matzka et al. 2021, doi:10.5880/Kp.0001). Sunspot number: WDC-SILSO, Royal Observatory of Belgium, Brussels, CC BY-NC 4.0. F10.7: Dominion Radio Astrophysical Observatory and Natural Resources Canada. NOAA Space Weather Prediction Center (U.S. Government material).
- IERS EOP 20 C04 (IERS Earth Orientation Centre, Observatoire de Paris); finals2000A (IERS Rapid Service/Prediction Centre, U.S. Naval Observatory). JPL planetary ephemeris DE440 (Park et al. 2021, doi:10.3847/1538-3881/abd414), via NASA/JPL NAIF. EGM2008: NGA (Pavlis et al. 2012, doi:10.1029/2011JB008916). EGM96: NASA GSFC and NIMA (Lemoine et al. 1998). FES2004 (Lyard et al. 2006) as distributed with the IERS Conventions (2010).

## References

### R1

Edgesource. Orbit accuracy experiments: frozen plans, run manifests, generated reports and data licenses, with a browser site that re-runs them. Commit 357d992d6c62daac4ccbb2fd230634ac292192b8. [Repository](https://github.com/DigitalArsenal/orbit-accuracy-experiments), [site](https://digitalarsenal.github.io/orbit-accuracy-experiments/)

### R2

Edgesource. HAC data sources: 97 public and login-only sources, their access, terms and use, and what is public about the HAC. [Inventory](https://github.com/DigitalArsenal/orbit-accuracy-experiments/blob/357d992d6c62daac4ccbb2fd230634ac292192b8/docs/hac-sources.md)

### R3

Licata, R. J., Mehta, P. M., Tobiska, W. K., Bowman, B. R., and Pilinski, M. D. CHAMP and GRACE-A density estimates with associated HASDM and JB2008 predictions. Zenodo, 2021. [doi:10.5281/zenodo.4602380](https://doi.org/10.5281/zenodo.4602380)

### R4

SpaceMapper. AOE catalog and orbital database. [AOE catalog](https://spacemapper.cn/en-us/catalog/aoecat/)

### R5

JSC Vimpel and Keldysh Institute of Applied Mathematics. Orbit parameters of newly detected HEO space debris objects. [Bulletin](https://spacedata.vimpel.ru/en/)

### R6

Vallado, D. A. Fundamentals of Astrodynamics and Applications, 4th edition. Microcosm Press, 2013.

### R7

Edgesource. Vimpel epoch conversion, refinement study and 64-object audit. SDN modules repository, commit 49e159d7003cac3d3e65b03170f11909fa86dd2c, `analysis/catalog-composer/docs/`. Private repository, available on request ([tj@edgesource.com](mailto:tj@edgesource.com)).

### R8

Vallado, D. A., Crawford, P., Hujsak, R., and Kelso, T. S. Revisiting Spacetrack Report #3. AIAA 2006-6753, 2006. [Paper](https://celestrak.org/publications/AIAA/2006-6753/AIAA-2006-6753.pdf)

### R9

Hoots, F. R., and Roehrich, R. L. Models for Propagation of NORAD Element Sets. Spacetrack Report No. 3, 1980. [Report](https://celestrak.org/NORAD/documentation/spacetrk.pdf)

### R10

Edgesource. SDN modules repository (space-data-network-modules), commit af637a8f4f4d6194f8aed9b7286a877d16670d9e: `propagator/hpop/README.md` (force model, Orekit suite and its measured agreement, resident path, catalog cost), `propagator/hpop/tests/evidence/orekit/` (the agreement by case; HPOP 1.3.0, `propagator/hpop/dist/isomorphic/module.wasm`, SHA-256 9c24bf0ed74f59893282514f08891c414da6db071d4997357a8ebd6c2a8a99e3), `analysis/gp-error-model/docs/hpop-calibration-2026-08.md`, `analysis/conjunction-assessment/docs/hpop-full-force-screen-2026-10.md`, `analysis/maneuver-detection/README.md`, `docs/studies/maneuver-detection.md`. Private repository, available on request ([tj@edgesource.com](mailto:tj@edgesource.com)).

### R11

Petit, G., and Luzum, B., editors. IERS Conventions (2010). IERS Technical Note 36. [Technical note](https://iers-conventions.obspm.fr/conventions/content/tn36.pdf)

### R12

Johnston, G., Riddell, A., and Hausler, G. The International GNSS Service. Springer Handbook of Global Navigation Satellite Systems, 967–982, 2017. [Products](https://igs.org/products/)

### R13

Pearlman, M. R., et al. The ILRS: approaching 20 years and planning for the future. Journal of Geodesy 93, 2161–2180, 2019. [doi:10.1007/s00190-019-01241-1](https://doi.org/10.1007/s00190-019-01241-1)

### R14

Dyreby, K., Caldas, F., and Soares, C. Analyzing Data Quality and Decay in Mega-Constellations: A Physics-Informed Machine Learning Approach. arXiv:2510.11242, 2025. [Preprint](https://arxiv.org/abs/2510.11242)

### R15

Jankovic, D. How long can you trust a Starlink TLE? An empirical comparison of SGP4 and high-fidelity propagation against operator-updated truth across a megaconstellation. arXiv:2605.19850, 2026. [Preprint](https://arxiv.org/abs/2605.19850)

### R16

Tapley, B. D., Schutz, B. E., and Born, G. H. Statistical Orbit Determination. Elsevier Academic Press, 2004.

### R17

Ly, D., Lucken, R., and Giolito, D. Correcting TLEs at epoch: application to the GPS constellation. Journal of Space Safety Engineering 7(3), 2020.

### R18

Hallgarten La Casta, M. I., and Amato, D. Generalisable bias correction of two-line element sets in the medium Earth orbit regime. 9th European Conference on Space Debris, 2025.

### R19

Levit, C., and Marshall, W. Improved orbit predictions using two-line elements. Advances in Space Research 47(7), 1107–1115, 2011. [arXiv:1002.2277](https://arxiv.org/abs/1002.2277)

### R20

Bowman, B. R., Tobiska, W. K., Marcos, F. A., Huang, C. Y., Lin, C. S., and Burke, W. J. A New Empirical Thermospheric Density Model JB2008 Using New Solar and Geomagnetic Indices. AIAA 2008-6438, 2008. [doi:10.2514/6.2008-6438](https://doi.org/10.2514/6.2008-6438)

### R21

Storz, M. F., Bowman, B. R., Branson, J. I., Casali, S. J., and Tobiska, W. K. High accuracy satellite drag model (HASDM). Advances in Space Research 36(12), 2497–2505, 2005. [doi:10.1016/j.asr.2004.02.020](https://doi.org/10.1016/j.asr.2004.02.020)

### R22

Doornbos, E. Empirical Model Calibration. In Thermospheric Density and Wind Determination from Satellite Dynamics, Springer Theses, 2012. [doi:10.1007/978-3-642-25129-0_6](https://doi.org/10.1007/978-3-642-25129-0_6)

### R23

Picone, J. M., Emmert, J. T., and Lean, J. L. Thermospheric densities derived from spacecraft orbits: accurate processing of two-line element sets. Journal of Geophysical Research 110(A3), 2005. [doi:10.1029/2004JA010585](https://doi.org/10.1029/2004JA010585)

### R24

Gondelach, D. J., and Linares, R. Real-time thermospheric density estimation via two-line element data assimilation. Space Weather 18(2), e2019SW002356, 2020. [doi:10.1029/2019SW002356](https://doi.org/10.1029/2019SW002356)

### R25

Gershman. Near Real-Time Thermospheric Density Monitoring from Public Satellite Tracking and Space Weather Data. NASA, 2026. [doi:10.64631/vfnq1575](https://doi.org/10.64631/vfnq1575)

### R26

Ou, Zhong, Hao, Li, et al. Near-Real-Time Global Thermospheric Density Variations Unveiled by Starlink Ephemeris. Remote Sensing 17(9), 1549, 2025. [doi:10.3390/rs17091549](https://doi.org/10.3390/rs17091549)

### R27

Yamamoto. Tomography of thermospheric density from Starlink Ephemeris: initial report. Earth, Planets and Space 78, 2026. [doi:10.1186/s40623-026-02509-5](https://doi.org/10.1186/s40623-026-02509-5)

### R28

Fitzpatrick, Sutton, Pilinski, and Palo. Harnessing Satellite Constellations as Signals of Opportunity for an Enhanced Specification of the Satellite Drag Environment in the Critical LEO Regime. Space Weather, 2026. [doi:10.1029/2025SW004611](https://doi.org/10.1029/2025SW004611)

### R29

Gondelach, D. J., and Linares, R. Real-Time Thermospheric Density Estimation via Radar and GPS Tracking Data Assimilation. Space Weather 19, e2020SW002620, 2021. [doi:10.1029/2020SW002620](https://doi.org/10.1029/2020SW002620)

### R30

Lemmens, S., and Krag, H. Two-Line-Elements-Based Maneuver Detection Methods for Satellites in Low Earth Orbit. Journal of Guidance, Control, and Dynamics 37(3), 860–868, 2014. [doi:10.2514/1.61300](https://doi.org/10.2514/1.61300)

### R31

Willsky, A. S., and Jones, H. L. A generalized likelihood ratio approach to the detection and estimation of jumps in linear systems. IEEE Transactions on Automatic Control 21(1), 108–112, 1976. [doi:10.1109/TAC.1976.1101146](https://doi.org/10.1109/TAC.1976.1101146)

### R32

Zaidi, W. H., and Hejduk, M. D. Earth Observing System Covariance Realism. AIAA/AAS Astrodynamics Specialist Conference, AIAA 2016-5628, 2016. [doi:10.2514/6.2016-5628](https://doi.org/10.2514/6.2016-5628)

### R33

Osweiler, V. P. Covariance estimation and autocorrelation of NORAD two-line element sets. MS thesis, Air Force Institute of Technology, AFIT/GSS/ENY/06-M09, 2006. [Thesis](https://scholar.afit.edu/etd/3531/)

### R34

Geul, J., Mooij, E., and Noomen, R. TLE uncertainty estimation using robust weighted differencing. Advances in Space Research 59(10), 2522–2535, 2017. [doi:10.1016/j.asr.2017.02.038](https://doi.org/10.1016/j.asr.2017.02.038)

### R35

Thompson, Gossner, Sais, and Cunningham. Fast covariance propagation for two-line element sets. AMOS Technical Conference, 2019. [doi:10.64861/twqd5985](https://doi.org/10.64861/twqd5985)

### R36

Verhaeghe, De Clerk, Lauwens, Delabie, Vandepitte, and Vandenbussche. Predicting TLE covariance using transformer-based time series forecasting. Small Satellite Conference, SSC25-IV-03, 2025. [Paper](https://digitalcommons.usu.edu/cgi/viewcontent.cgi?article=6135&context=smallsat)

### R37

Hejduk, M. D. ASW Covariance Introduction and Formation. NASA, 2016. [NTRS 20160005042](https://ntrs.nasa.gov/citations/20160005042)

### R38

Hejduk, M. D. Three CA-Related Covariance Issues and Their Solutions. NASA, 2019. [NTRS 20190026790](https://ntrs.nasa.gov/citations/20190026790)

### R39

Space Data Standards. Legacy message survey: the Vector Covariance Message format and sample; PRW and VCM schemas. [Survey](https://github.com/DigitalArsenal/spacedatastandards.org/tree/main/survey/legacy-messages/vcm)

### R40

NASA Goddard Space Flight Center. General Mission Analysis Tool (GMAT), atmosphere models. [Project](https://gmat.gsfc.nasa.gov/)

### R41

Pachura, D., and Hejduk, M. D. Atmospheric Model at JSpOC. NASA, 2016. [NTRS 20160005044](https://ntrs.nasa.gov/citations/20160005044)

### R42

National Research Council. Continuing Kepler's Quest: Assessing Air Force Space Command's Astrodynamics Standards. The National Academies Press, 2012. [doi:10.17226/13456](https://doi.org/10.17226/13456)

### R43

Berry, M. M., and Healy, L. M. Implementation of Gauss–Jackson Integration for Orbit Propagation. Journal of the Astronautical Sciences 52(3), 331–357, 2004. [Paper](https://ui.adsabs.harvard.edu/abs/2004JAnSc..52..331B/abstract)

### R44

CS GROUP. Orekit 13.1, open-source space flight dynamics library, Apache License 2.0. [Project](https://www.orekit.org/)

### R45

Roberts, C. E., Jr. An analytic model for upper atmosphere densities based upon Jacchia's 1970 models. Celestial Mechanics 4, 368–377, 1971. [doi:10.1007/BF01231398](https://doi.org/10.1007/BF01231398)

### R46

Julier, S. J., and Uhlmann, J. K. A non-divergent estimation algorithm in the presence of unknown correlations. Proceedings of the American Control Conference, 2369–2373, 1997.

### R47

Edgesource. VCM adapter (`analysis/vcm-adapter`). SDN modules repository, commit 1879d8c5c17c061e157c100723a8423c59fd98e2. Private repository, available on request ([tj@edgesource.com](mailto:tj@edgesource.com)). Parity record: [vcm-parity.md](https://github.com/DigitalArsenal/orbit-accuracy-experiments/blob/357d992d6c62daac4ccbb2fd230634ac292192b8/docs/vcm-parity.md)

### R48

CelesTrak. Supplemental GP element sets: methodology for fitting operator ephemerides with SGP4. [Supplemental GP](https://celestrak.org/NORAD/elements/supplemental/)

### R49

NASA. Spacecraft Conjunction Assessment and Collision Avoidance Best Practices Handbook, Vol. 2, Technical Appendices, August 2026. [NTRS 20260000457](https://ntrs.nasa.gov/citations/20260000457)

### R50

Koury, A. Fast All-vs-All Conjunction Screening. Space Data Network technical whitepaper. [Paper](fast-conjunction-assessment.md)

[v1]: https://github.com/DigitalArsenal/orbit-accuracy-experiments/blob/357d992d6c62daac4ccbb2fd230634ac292192b8/results/v1/README.md
[e1]: https://github.com/DigitalArsenal/orbit-accuracy-experiments/blob/357d992d6c62daac4ccbb2fd230634ac292192b8/results/e1/test/REPORT.md
[e2]: https://github.com/DigitalArsenal/orbit-accuracy-experiments/blob/357d992d6c62daac4ccbb2fd230634ac292192b8/results/e2/test/README.md
[e2b]: https://github.com/DigitalArsenal/orbit-accuracy-experiments/blob/357d992d6c62daac4ccbb2fd230634ac292192b8/results/e2b/README.md
[e3]: https://github.com/DigitalArsenal/orbit-accuracy-experiments/blob/357d992d6c62daac4ccbb2fd230634ac292192b8/results/e3/README.md
[e4]: https://github.com/DigitalArsenal/orbit-accuracy-experiments/blob/357d992d6c62daac4ccbb2fd230634ac292192b8/results/e4/README.md
[e5]: https://github.com/DigitalArsenal/orbit-accuracy-experiments/blob/357d992d6c62daac4ccbb2fd230634ac292192b8/results/e5/test/REPORT.md
[e6]: https://github.com/DigitalArsenal/orbit-accuracy-experiments/blob/357d992d6c62daac4ccbb2fd230634ac292192b8/results/e6/README.md
[e7]: https://github.com/DigitalArsenal/orbit-accuracy-experiments/blob/357d992d6c62daac4ccbb2fd230634ac292192b8/results/e7/test/REPORT.md
[e8]: https://github.com/DigitalArsenal/orbit-accuracy-experiments/blob/357d992d6c62daac4ccbb2fd230634ac292192b8/results/e8/test/REPORT.md
[e15]: https://github.com/DigitalArsenal/orbit-accuracy-experiments/blob/4b0bc1b556dae67d55c1d81a31337738822e7e06/results/e15/README.md
[e16]: https://github.com/DigitalArsenal/orbit-accuracy-experiments/blob/30da10c9d913abc4db45c468d97e0033c045afbd/results/e16/test/REPORT.md
[xp]: https://github.com/DigitalArsenal/space-data-network-modules/blob/1606dddebb112ff97feea050de65601bdcf4ae43/propagator/sgp4-xp/tests/measure/README.md
[e13]: https://github.com/DigitalArsenal/orbit-accuracy-experiments/blob/357d992d6c62daac4ccbb2fd230634ac292192b8/results/e13/README.md
[e14]: https://github.com/DigitalArsenal/orbit-accuracy-experiments/tree/357d992d6c62daac4ccbb2fd230634ac292192b8/results/e14

### R51

COMSPOC. Spacebook XP-TLE: extended-perturbation element sets fitted to Spacebook's own orbits. [Page](https://spacebook.com/xp-tle)

### R52

U.S. Space Force. Astrodynamics Standards: Sgp4Prop 9.1.1.0, with SGP4 and SGP4-XP. Distributed through Space-Track. [Documentation](https://www.space-track.org/documentation#/astro)

### R53

Space-Track.org. User Agreement: "A TLE available to the public should not be used for conjunction assessment prediction." [User Agreement](https://www.space-track.org/documentation#/user_agree)

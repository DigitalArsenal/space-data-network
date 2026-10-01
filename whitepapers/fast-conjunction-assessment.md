# Fast All-vs-All Conjunction Screening

Screening every catalog object against every other in seconds to minutes, for any propagator

Anthony "TJ" Koury III

Edgesource, Space Data Network · tj@edgesource.com

Technical whitepaper 1.0 | 1 October 2026

Numerical evidence cutoff: 1 October 2026

© Edgesource Corporation

## Executive summary

Space Data Network (SDN) screens the full public catalog, 32,514 objects, all-vs-all over three days in 19 s with SGP4 on ordinary processors, and in 21 s with a GPU. With numerical high-precision orbit propagation (HPOP) it takes 6 to 7 minutes, almost all of it propagation. The screen finds every pair that comes within 5 km, and reports each close approach's time (TCA), miss distance, relative speed and maximum collision probability. The measurements were taken on one 28-core workstation, in Node.js and in a standard web browser. In WasmEdge, the runtime SDN nodes use, the SGP4 screen takes 23 s. Every run on one catalog and propagator reports the same conjunctions.

Five design choices produce that speed:

1. **A spatial grid proposes pairs; the module decides.** At every 60 s step, a uniform grid finds the pairs that could be close among the 528.6 million, on a GPU (WebGPU) or in the WebAssembly (WASM) conjunction module on CPU threads. The module re-tests each candidate in f64 and refines it. Every reported number comes from the module.
2. **Each trajectory bounds its own motion.** Between two samples, an object cannot stray from a straight line by more than a bound its trajectory source computes. A pair whose straight-line approach stays farther apart than the threshold plus both bounds cannot close, and is discarded without further work. The bound holds for any force model, maneuvers included.
3. **One propagator per run, in time windows.** A run screens either SGP4 element sets or one numerical propagator's trajectories, never a mix. Long screens run as consecutive windows, so memory stays at one window's trajectories while the next window is propagated.
4. **Load once, move records unchanged.** The catalog loads into the module once. A propagator's output passes into the conjunction module as the records it emitted, without re-encoding.
5. **Refinement proves, then solves.** Where the range of a pair provably has one minimum, Newton's method on the range rate finds it in about ten evaluations, instead of a dense scan.

The screen reproduces the module's single-call screen where both can run. On 4,000 objects over one day, all 1,068 conjunctions match, with TCA within 0.64 ms and miss distance within 5 mm; the single call takes 32.9 s and the windowed screen 0.44 s. Reported probability is the covariance-free maximum. No calibrated covariance or covariance-based probability of collision is claimed (section 7, [R1](#r1)).

## 1 The problem

An all-vs-all screen compares every object with every other: n(n − 1)/2 pairs, 528.6 million for 32,514 objects. A three-day screen at 60 s steps evaluates each pair at 4,321 instants, about 2.3 trillion pair-steps. A close approach at 10 km/s relative speed lasts under a second inside a few kilometers, so a coarse test that misses it loses the event.

The module's existing single-call screen (`screen_catalog`) uses a perigee/apogee prefilter and a k-d tree per time step. It holds every encounter of the window in WASM linear memory, at most 2 GiB. It runs out of memory above about 7,000 objects for a one-day window ([R2](#r2)).

## 2 Pipeline

<p align="center">
  <img src="assets/ca-pipeline.svg" alt="Pipeline: load index, coarse grid, pair search on the GPU or in the module, refine, and merge windows; SGP4 element sets load once, a propagator's trajectories load per window" width="720"/>
</p>

The host code (JavaScript) moves records between the module and the GPU and contains no physics. The conjunction module, the SGP4 implementation, the HPOP propagator and the epoch-state converter are WASM modules. They are built with the SDN Module SDK and run unchanged in a browser, in the WasmEdge runtime and in its Docker image ([R3](#r3)).

| Stage | Where | Precision | Decides |
| --- | --- | --- | --- |
| Coarse grid | WASM module, threaded | f64 computed, f32 stored | Sample states and bounds |
| Pair search | GPU, WebGPU compute | f32 with 0.01 km slack | Candidates only |
| Pair search without a GPU | WASM module, threaded | f64 | Candidates only |
| Re-test and refinement | WASM module, threaded | f64 | Every reported result |
| Window merge | Host | Exact comparisons | Which window owns each TCA |

## 3 The candidate test

### Self-bounded motion

Let h be half the coarse step (30 s at 60 s steps). For a sample of an object at step k, with position r and velocity v, the trajectory source supplies a bound D such that the true position stays within D of the straight line:

$$
\lvert \mathbf{r}(t_k+\tau) - \mathbf{r}_k - \mathbf{v}_k\tau \rvert \le D \quad \text{for all } \lvert\tau\rvert \le h.
$$

Two objects can then come within the threshold d during the interval only if the straight-line relative path does so within d + D₁ + D₂. The test computes the clamped closest approach of the relative straight line over [−h, h] and compares it with that limit. A pair that fails the test at every step of an interval cannot close in it.

Each source kind computes D from what it knows:

| Source | Bound | Basis |
| --- | --- | --- |
| SGP4 element set | ½ A h², with A = 1.05 μ / r²_min and r_min = \|r\| − \|v\| h, floored at 6,000 km | SGP4 motion is unpowered; the 5 % margin covers J2 (0.16 % at the surface) and drag |
| Chebyshev trajectory (PPE) from any propagator | ½ A h² + (J + G) h + P | A bounds the position series' second derivative using \|T_k″\| ≤ k²(k² − 1)/3. J and P sum velocity and position jumps where intervals meet. G is the gap between the velocity series and the position series' derivative |

The trajectory bound uses only the coefficients, so it holds for any force model and for impulsive maneuvers that appear as interval jumps. A native test checks it against the trajectory itself every 0.1 s around samples taken every 7 s. The trajectory is a 7,000 km orbit interpolated as a propagator would export it, with a 5 m/s maneuver at an interval boundary. Tested at h = 30 s and h = 90 s, the bound was never exceeded, and away from the maneuver it was at most 1.34 times the true deviation ([R2](#r2)).

### Spatial grid

Testing all 528.6 million pairs at every step is wasted work: almost all pairs are thousands of kilometres apart. At each step, an object's straight-line segment over [−h, h], widened by d/2 + D, gives an axis-aligned box. Two objects can pass the straight-line test only if their boxes overlap, so a search over overlapping boxes drops nothing the test would pass.

The boxes go into a uniform grid whose cell is the 99th-percentile box edge. Each overlapping pair is counted once, in the cell holding the low corner of the two boxes' overlap. The few boxes that span four or more cells on an axis, such as fast perigee passes, are tested against every object. The same grid runs in two places:

- **On a GPU**, in three WebGPU compute passes per step. The first inserts each box into its cells, hashed into one lock-free list per slot. The second has each object walk its cells' lists. The third tests the large boxes.
- **Without a GPU**, in the WASM module on CPU threads: in a browser without WebGPU, in Node.js, or in WasmEdge on an SDN node or in Docker.

Either way, the grid proposes exactly the pairs an all-pairs test would. On the full catalog over three days, the GPU grid proposed the same 2,678,533 candidates as an all-pairs GPU kernel did.

SDN nodes run modules in WasmEdge compiled ahead of time. That needs SDN's patched WasmEdge 0.16.4: the stock compiler mishandles offsets on atomic memory instructions, and with it the full-catalog search hung in its second window. Interpreted WasmEdge is about 75 times slower.

### Two precisions, one decision

The GPU evaluates the test in f32. Positions near 7,000 km round to about 0.5 m in f32, so the GPU adds 0.01 km of slack and proposes a superset. The module repeats the test in f64 for every candidate. Over one day of the full catalog the GPU proposed 829,990 candidates and 828,910 passed the f64 test. Over three days the GPU proposed 2,678,533 and the CPU grid, which applies the f64 test directly, 2,675,123; both refine to the same conjunctions.

### Refinement

Passing steps of a pair join into encounters. Each encounter is refined by the same TCA search the single-call screen uses, so both report the same TCA.

For most encounters the search first proves that the range has one minimum in its window, then solves for it directly. Let A bound both objects' accelerations over the window; for SGP4 this is 1.05 μ / r²_min, the bound of the candidate test. With T the window's half-width and Δr, Δv the relative state at its middle:

$$
\lvert \Delta\dot{\mathbf r} \rvert \ge \lvert \Delta\mathbf v \rvert - \varepsilon - A T, \qquad \lvert \Delta\mathbf r \rvert \le \lvert \Delta\mathbf r_{\mathrm{mid}} \rvert + \lvert \Delta\mathbf v \rvert T + \tfrac12 A T^2 .
$$

ε (1 m/s) allows for a source's velocity differing from the rate of its position. If q = |Δr|max A / |Δṙ|min² < 1, the squared range is convex on the window. Its one minimum is then the root of the range rate f = Δr · Δv, and Newton steps of −f / |Δv|² converge by a factor q or better per step. About ten state evaluations reach the root to the resolution of a Julian date, about 47 µs.

Where the proof fails, for example a slow pair that stays close for minutes, the search samples the range every 5 s and refines each minimum it brackets with golden-section search. The earlier version used that path for every encounter. It also rescanned ±60 s at 0.05 s around each candidate minimum, tens of thousands of SGP4 evaluations per conjunction. Section 6 measures the change.

Probability uses the Alfano maximum ([R4](#r4)). When a refine call would stage more than the module's 16,384-event output limit, the host splits it in two.

## 4 One propagator per run

A run screens one propagator's output. A resident index holds either mean elements, evaluated with the module's SGP4, or trajectories from one generator, identified by the Chebyshev ephemeris (PPE) record's `EPHEMERIS_SOURCE`. The module refuses an index that mixes them (`mixed-propagators`).

Trajectories reach the module as their propagator exported them. The HPOP module's export is a size-prefixed `$PRW` record per object. The conjunction module's index preparation takes those records on its `trajectories` port without re-encoding, and the request carries only each object's identity. Trajectories in TT or TDB map to UTC linearly between their converted interval ends, using the vendored ERFA library ([R5](#r5)). TDB − TT is evaluated once per minute and interpolated, exact to about 1e-14 s.

## 5 Time windows

A long screen runs as consecutive windows; the merged result equals one screen of the whole span:

- A conjunction belongs to the window that holds its TCA.
- A TCA within the refinement tolerance of a shared edge is reported by both windows and kept once.
- An object excluded in any window, because its propagator cannot cover it there, has no conjunctions anywhere in the span. This matches the single-call screen's exclusion rule.

Window length trades memory for per-window overhead. A day of HPOP's trajectories is about 90 KB per object; a 2-hour window of the full catalog is about 400 MB. SGP4 windows reuse one index loaded at the start, so they are longer (6 hours).

Tests check four windows against a single screen for both propagators. The SGP4 case uses CelesTrak element sets that include an object SGP4 cannot propagate. The HPOP case uses HPOP-integrated crossing orbits passed through the `trajectories` port ([R2](#r2)).

### The HPOP propagation farm

1. The epoch-state module converts each element set into a GCRF state at its epoch: SGP4 at zero elapsed time, then TEME to GCRF through ERFA. This is the TLE-to-numerical handoff of the companion paper, section 5 ([R1](#r1)).
2. HPOP resident instances, at most 1,024 objects each, integrate those states on worker threads. Objects are dealt round-robin into about two instances per worker, so element sets of every age spread evenly.
3. Each window, every instance exports the window and the records go to the browser unchanged. The next window is exported while the current one is screened.

HPOP integrates each object from its element epoch, so the first window carries that catch-up. HPOP drops cached intervals that end before the requested window, so an instance holds about one window per object.

## 6 Results

All runs used the Space-Track GP catalog of 30 September 2026, 32,514 objects ([R6](#r6)). Screens started 2026-10-01T00:00Z, with a 5 km threshold and 60 s coarse steps, on a Mac Studio with 28 cores and 27 module threads. GPU runs used headless Chrome with Metal for WebGPU. The workstation was shared with other work (1-minute load average 15–65 on 28 cores), so times are upper bounds.

### Full catalog, three days

| Propagator | Pair search | End to end | Sampling and search | Refine | Conjunctions |
| --- | --- | ---: | ---: | ---: | ---: |
| SGP4, 12 × 6 h | CPU, Node.js | 19.1 s | 9.7 s | 6.6 s | 292,516 |
| SGP4 | CPU, WasmEdge (AOT, SDN patches) | 22.9 s | 11.9 s | 8.7 s | 292,516 |
| SGP4 | GPU | 21.3 s | 6.1 + 2.3 s | 7.2 s | 292,516 |
| HPOP, 36 × 2 h | GPU | 382.1 s | 4.7 + 1.8 s | 8.3 s | 301,396 |
| HPOP | CPU, Node.js | 437.8 s | 15.2 s | 8.7 s | 301,396 |

- For each propagator, every run reports the same conjunctions with the same TCA and miss distance. SGP4 excluded 25 objects it could not propagate in the span; HPOP excluded none.
- **SGP4's** remaining time is sampling: 4,321 steps × 32,514 SGP4 evaluations. On the GPU path the module samples (6.1 s) and the GPU searches (2.3 s).
- **HPOP's** time is propagation: the screen waits 350–400 s for the propagation farm, including about 120 s of catch-up from element epochs in the first window. Screening and refining take under 30 s.
- The two propagators report different conjunctions. Neither result is a measure of accuracy (section 7).

### Step size

The candidate test is exhaustive at any step, so the step size trades sampling against candidates. SGP4, three days:

| Step | Candidates | CPU search | CPU end to end | GPU end to end | Conjunctions |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 5 s | 4,748,277 | 92.1 s | 102.0 s | 98.6 s | 292,516 |
| 30 s | 1,414,694 | 17.8 s | 29.9 s | | 292,516 |
| 60 s | 2,675,123 | 9.7 s | 19.1 s | 21.3 s | 292,516 |
| 120 s | 17,116,089 | 10.0 s | 39.4 s | | 292,516 |

Every step size reports the same conjunctions, with miss distances within 2 mm. TCAs differ by up to 173 ms, but only on flat minima, where the miss distance is the same. A 5 s screen samples 12 times as often and finds nothing more. Shorter steps cost sampling; longer steps give larger bounds and more candidates. 60 s is near the optimum.

### How the time came down

Three days, SGP4, CPU search in Node.js, same host:

| Change | End to end | Refine |
| --- | ---: | ---: |
| Dense TCA scan around every minimum | 131.4 s | 114.3 s |
| Unimodal proof, golden-section search | 63.9 s | 45.6 s |
| Newton steps on the range rate | 55.3 s | 38.4 s |
| The same solve for each fast encounter's first pass | 34.7 s | 17.6 s |
| SGP4 state read without a lock; element sets converted once | 25.2 s | 12.7 s |
| Refinement work shared in small chunks; events built in parallel | 19.1 s | 6.6 s |

Every row reports the same 292,516 conjunctions. On the GPU path, replacing the all-pairs kernel with the grid cut the GPU search from 8.5 s to 2.3 s.

The first version also sent the whole catalog with every call. Loading it once into the module cut one day of the full catalog from 266.7 s to 55.4 s, with the same 77,667 conjunctions.

### Agreement with the single-call screen

| Objects, window | Single call | Windowed, CPU search | Conjunctions | Largest difference |
| --- | ---: | ---: | --- | --- |
| 2,000, 0.1 day | 1.1 s | 0.08 s | 23 of 23 | TCA 0.16 ms, miss 0.6 mm |
| 4,000, 1 day | 32.9 s | 0.44 s | 1,068 of 1,068 | TCA 0.64 ms, miss 5 mm |

On the 2,000-object case the CPU search and the GPU found the same 424 candidates and 23 conjunctions, in Node.js, WasmEdge and a browser. Differences from the single call come from refinement starting from different coarse brackets, and sit inside the module's parity tolerances of 10 ms and 10 cm. Against the earlier dense scan, Newton's method found a deeper minimum for 225,968 of the 292,516 conjunctions. No TCA moved more than 5.4 ms and no miss distance more than 24 cm.

### Validation findings

The work found two defects in existing code, both fixed:

- The module's generic pair search, used for tabulated and polynomial trajectories, sampled the range at the coarse step. It missed fast crossings: for one pair it reported a 41.9 km approach and missed a 500 m meeting. It now uses the same 5 s search as the SGP4 path.
- HPOP could not export a window starting on one of its 10-minute interval boundaries after the first. A Julian date resolves to about 4.7e-10 day, and the interval lookup's 1e-12-day check refused the boundary.

## 7 Uncertainty and probability of collision

The reported probability is the Alfano maximum: the largest probability any covariance size could give for the reported miss distance and a 10 m combined hard-body radius ([R4](#r4)). It needs no covariance and serves as an upper bound. The companion paper sets the conditions this screen respects ([R1](#r1), sections 5, 9, 12 and 16.2):

- A TLE supplies no covariance. The initial uncertainty of a TLE-seeded HPOP trajectory, including this paper's HPOP runs, is unknown until a validated representation exists.
- A probability of collision requires relative-state uncertainty with independent calibration and stated cross-correlation, geometry and hard-body-radius assumptions. This paper claims no calibrated covariance and no covariance-based probability.

The planned path follows the companion paper's three tasks:

1. **Initial covariance P₀** from operator products and conjunction messages that carry covariance, from SDN orbit-determination fits (formal covariance, marked uncalibrated), or from an empirical model built from element-set history and checked against independent reference orbits.
2. **Propagation** P(t) = Φ P₀ Φᵀ + Q with a declared process noise Q, exported with each window's trajectories. The conjunction module already implements covariance-based probability methods (Foster, Patera, Chan, Alfriend, Alfano 2005, Laas 2015), and only reported conjunctions need them.
3. **Calibration gates** that measure coverage against held-out evidence before any covariance-based probability is published as more than conditional on its assumptions.

Admissible-set screening with TEAG and the ESPF is a separate path. It reports possibility and necessity, which are not collision probabilities ([R1](#r1), section 9).

## 8 Limits and next work

| Limit | Effect | Next step |
| --- | --- | --- |
| SGP4 sampling | About half of a 19 s screen: 140 million SGP4 evaluations | Fewer evaluations per step only with a bound that stays exhaustive |
| HPOP propagation | Over 90 % of the HPOP screen | Force-model choices and initial states nearer the screen start; both are modeling decisions with accuracy consequences |
| Catch-up from element epochs | 123 s before the first HPOP window | Persistent propagation across screens |
| Memory per window | About 400 MB per 2-hour HPOP window | Window length chosen per host |
| One host | All timings from one shared 28-core workstation | Repeat on other hosts, GPUs and Docker containers ([R2](#r2) includes the procedure) |
| Accuracy | Not assessed here | The companion paper's validation baselines ([R1](#r1), section 12) |

This paper reports computation speed and agreement between implementations. It does not establish operational readiness, catalog accuracy, conjunction accuracy against independent truth, or agreement with SOCRATES ([R7](#r7)).

## References

### R1

Koury, A. and Jah, M. K. Evidence-Supported ASO Catalog. Space Data Network technical whitepaper 1.8.1, revised 1 October 2026. [Paper](evidence-supported-aso-catalog.md)

### R2

Edgesource. Conjunction assessment module: all-vs-all screening on a GPU or on CPU threads, time windows, TCA solve, parity and bound tests, and the benchmark procedure. Modules commit c592acb618c287f1c12f2c3bb28f59e6c556fd34. [Method](https://github.com/DigitalArsenal/space-data-network-modules/blob/c592acb618c287f1c12f2c3bb28f59e6c556fd34/analysis/conjunction-assessment/docs/gpu-all-vs-all.md) · [Benchmark](https://github.com/DigitalArsenal/space-data-network-modules/blob/c592acb618c287f1c12f2c3bb28f59e6c556fd34/analysis/conjunction-assessment/docs/benchmark.md)

### R3

Edgesource. Space Data Module SDK 0.8.26. [Repository](https://github.com/DigitalArsenal/space-data-module-sdk)

### R4

Alfano, S. Relating Position Uncertainty to Maximum Conjunction Probability. AAS 03-548, 2003. [Paper](https://celestrak.org/SOCRATES/AIAA-03-548.pdf)

### R5

ERFA (Essential Routines for Fundamental Astronomy), BSD-3, derived with permission from IAU SOFA. Vendored in the modules repository. [Project](https://github.com/liberfa/erfa)

### R6

Space-Track.org. General perturbations element sets, catalog snapshot of 30 September 2026. [Service](https://www.space-track.org)

### R7

CelesTrak. SOCRATES conjunction screening service. [Service](https://celestrak.org/SOCRATES/)

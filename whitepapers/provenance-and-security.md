# Provenance and Security in Space Data Network

Records that name their sources, results anyone can re-run, screening that checks itself, and screening that keeps orbits private

Anthony "TJ" Koury III and Claude Opus 5.5

Koury: Edgesource, Space Data Network · tj@edgesource.com. Claude: Anthropic.

Technical whitepaper 1.0 | 11 October 2026

Numerical evidence cutoff: 11 October 2026

---

## 1 Summary

Two companion papers report the physics: how Space Data Network (SDN) builds and measures its orbital catalog<sup>[1](#r1)</sup>, and how it screens that catalog for close approaches<sup>[2](#r2)</sup>. This paper covers what makes those results traceable, checkable and safe to share:

1. **Records.** Every input keeps its provider, edition and the SHA-256 of its bytes, and every solution names the records it rests on (section 2).
2. **Publication and reproduction.** A catalog snapshot is published whole, signed and content-addressed. Every experiment's numbers trace to a run manifest, and the same modules re-run them in a web browser (section 3).
3. **Screening that checks itself.** Screening work units name every input by hash, and their outputs are byte-identical across runtimes, so replicas check each other. In a local test, a worker that hid conjunctions was caught and its work redone (section 4).
4. **Private screening.** Two operators learn when their objects come close without exchanging trajectories, by computing distances on homomorphically encrypted positions (section 5).
5. **Keys.** A signature is only as good as the key behind it. SDN's keys carry the security bond of the Adversarial Security paper, which makes a key's integrity publicly checkable<sup>[3](#r3)</sup> (section 6).

## 2 Records and lineage

A provider's ephemeris or element set already embeds the provider's models and inference, so the catalog keeps it as an attributed provider claim. Agreement between products counts as independent confirmation only when their lineage is independent; unknown lineage is recorded as unknown.

A source record keeps the provider, native identifier, edition, SHA-256 of the bytes, product type, epoch, time scale and frame; observation time, solution epoch, issue time and retrieval time stay distinct. A solution record specifies:

| Field | Content |
| --- | --- |
| State and validity | State or ephemeris, epoch, applicable interval |
| Evidence and lineage | Source records, their levels and dependencies, editions |
| Dynamics | Force model, environment inputs, parameters estimated, considered or assumed |
| Measurement model | Sensor geometry, timing, calibration, biases; unknown when a provider does not state it |
| Inference | Estimator or selection rule, weights, priors, software version |
| Uncertainty | Covariance, its source and its calibration label |

Hashes establish byte identity and signatures establish attribution; physical validity rests on the measurements of the catalog paper<sup>[1](#r1)</sup>. Identical inputs, module binaries and policy reproduce the same decisions.

## 3 Publication and reproduction

Publication is transactional: acquire and hash the bytes, verify, normalize, validate, sign a manifest, then announce the new catalog head. Consumers read one complete, immutable snapshot; each update names the object, the prior and new solutions, the epoch, the recipe and the provenance. Content addressing and independent replication keep a snapshot retrievable from any node that holds it.

Each experiment's numbers come from a run manifest: the repository and module commits, each module's WebAssembly SHA-256 and the SHA-256 of every input; reports are generated from metrics<sup>[4](#r4)</sup>. The experiments' public site runs the same modules in a web browser: a V1 seed reproduces the committed result exactly, and any Orekit case re-runs from its exact request. Space-Track element sets, SpaceX and Planet files and SET's indices are cited by URL and SHA-256, not copied.

| Evidence | Commit |
| --- | --- |
| Experiments V1, E1–E8, E13 and E14 | orbit-accuracy-experiments `357d992d6c62daac4ccbb2fd230634ac292192b8` |
| HPOP 1.3.0, its Orekit suite, screen and covariance calibration; maneuver detection 0.3.0 | space-data-network-modules `d4a0b1155f64ac088694f9758c32e3b691952674` |
| HPOP in V1 | space-data-network-modules `750b6399173a2678b8e8d0885557976d17a9ad5c` |
| VCM adapter | space-data-network-modules `1879d8c5c17c061e157c100723a8423c59fd98e2` |
| Vimpel conversion and refinement | space-data-network-modules `49e159d7003cac3d3e65b03170f11909fa86dd2c` |

HPOP's WebAssembly binary in V1, SHA-256:

```text
473d527175de6606ccf305c45b37218288634fa9de2dcab0d72564a190bdabd1
```

## 4 Screening that checks itself

A screen splits into work units<sup>[2](#r2),[5](#r5)</sup>:

- **Grid units:** a block of catalog rows over a time window gives those rows' coarse grid, the screen's index.
- **Screen units:** a block of primary objects over a window gives the events of their pairs. Earlier blocks are skipped and the rest are secondary, so every pair is in exactly one unit.

A unit record names every input by SHA-256: the module, the catalog's element sets, the window's grid blocks and exclusions, the roles and the controls. Its identifier is the SHA-256 of the record. The conjunction module's outputs depend on neither the runtime nor the thread count, so two replicas of a unit must agree byte for byte. Volunteer computing otherwise compares floating-point results only between matched platforms<sup>[6](#r6),[9](#r9)</sup>; WebAssembly's determinism<sup>[8](#r8)</sup>, carried through a threaded module, removes that need. Each answer is filed by hash and announced with an SDS publish notification (PNM): the output's content identifier, the unit identifier and an Ed25519 signature.

In a local test, five worker processes acted as independent volunteers, each with its own module instance and key, and declared their threads and memory; a unit went only to a worker with the memory it needed. A keyed 30 % of units ran on two workers, on different runtimes where one was free. A mismatch went to a third worker, the majority won, the losing worker's answers stopped counting, and every unit that relied on them ran again<sup>[7](#r7)</sup>. On the 3-day screen of the 32,514-object catalog, 96 units:

| Measure | Result |
| --- | --- |
| Replicas compared among honest workers | 27, all byte-identical; 23 across runtimes (Node V8 and WasmEdge ahead-of-time) |
| A worker hiding the closest conjunction of half its screen units | Caught on 5 mismatches; its 9 altered outputs discarded; 13 units run again |
| A worker leaving 30 % of its units unanswered | Retired after 2 missed deadlines |
| A worker with 768 MiB | Given only the grid units it could hold |
| Conjunctions | 294,544; the event set's SHA-256 equals the single-process screen's |

Replication catches a dishonest worker only through a replicated unit it altered: with replication rate r, m altered units all escape with probability (1 − r)^m, 4 % for r = 0.3 and m = 9. Once caught, all of its work is redone.

An operator's node screens its own fresh data the same way and publishes the data and the results together. Its ephemerides are fitted to SGP4 element sets in memory, its objects' rows replace theirs in the network's grid, one screen unit runs per window, and a PNM signed by the operator's key names the manifest of element sets, unit records and outputs. An independent process in another runtime rebuilds each window's grid from the unit's sources, re-runs each unit and compares bytes. On 11 October 2026, 3 days ahead:

| | 64 Starlink satellites, SpaceX's files fitted here | The constellation, CelesTrak's fits |
| --- | --- | --- |
| From data in memory to the signed PNM | 9.1 s, 23 CPU-s | 45.9 s, 150 CPU-s |
| Independent check, WasmEdge ahead-of-time in a separate process | 12 of 12 window grids and 12 of 12 outputs byte-identical; signature valid | The same |

## 5 Private screening

Operators hold back their best data:
- **National security.** Classified orbits reveal capability and coverage.
- **Commercial value.** Constellation geometry and station-keeping are trade
  secrets.
- **Maneuver intent.** A planned burn tells competitors and adversaries that
  something is changing.
- **Liability.** Sharing creates obligations.

Without that data, the screen in this paper sees catalog estimates where
precise trajectories exist. Private screening lets two operators learn when
their objects come close without either seeing the other's trajectory.

This section specifies the protocol, measures its arithmetic, and analyses
what it reveals and how it can be abused<sup>[13](#r13)</sup>.

### What is computed on ciphertext

A (the requester) and B (the responder) each hold a trajectory, sampled on a
common time grid. A wants to know when B's object passes within R of its own.
The protocol uses homomorphic encryption: B computes on A's encrypted
positions without being able to read them<sup>[10](#r10),[11](#r11)</sup>.

1. **A encrypts its trajectory under its own key.**
   - The quantities are x, y and z, in integer metres.
   - 8,192 time steps are packed per ciphertext (BFV, n = 8192, 128-bit
     security).
   - Three 60-bit plaintext moduli, recombined by the Chinese remainder
     theorem, hold the range, so no value wraps.
2. **B computes on ciphertext.**
   - B squares A's coordinates itself: Enc(|a|²) = Enc(x)² + Enc(y)² +
     Enc(z)², once per submission, shared by all of B's objects.
   - Since |a − b|² = |a|² − 2a·b + |b|², B then forms Enc(|a − b|² − R′²)
     from products of its own plaintext with A's ciphertext.
   - A never supplies |a|². A false value would set the radius of every
     step's test, and the same value would pass the tube check (defense 2
     below).
3. **Both learn the result.** A secure comparison tells A and B, for each
   step, whether B is within R′.

A pair can come within R between two samples. Testing against
R′ = √(R² + (v_max Δt / 2)²) catches every such approach. With
v_max = 15.5 km/s, R′ is 9.2 km at 1 s steps and 77.7 km at 10 s steps.

**Key custody.**
- Only A ever decrypts, and no key leaves A.
- No third-party assessor is involved. A party holding the key that decrypts
  the result can also decrypt every input it receives under that key, so an
  assessor that decrypts adds trust without adding privacy.

### Rules of the exchange

**Fixed grid.**
- Every encrypted trajectory uses a grid set by the protocol: UTC-aligned
  windows, a fixed step Δt, and 8,192 steps per encrypted block.
- Slot j always means t₀ + jΔt, so a requester cannot choose its own sample
  times.

**One answer per pair and window.**
- B answers once per (counterpart object, window).
- It checks every step of the window, never a subset.
- A accepts one digitally signed response per pair, so it cannot ask twice
  and keep the better answer.

**Completeness audit.** Two stages:
1. **Structure, before answering, without decrypting.** B checks:
   - the window identifier and start epoch;
   - the step;
   - the number of encrypted blocks per coordinate (steps ÷ 8,192);
   - the encryption parameters.

   A short or malformed submission is refused.
2. **Content, under encryption.** A missing or zero-filled step is still a
   valid ciphertext, so structure alone cannot reveal it. The physics check
   (defense 1 below) tests every step, and a blank or invented step does not
   obey gravity.

**What can be verified under encryption.**
- **Before answering:**
  - the structure;
  - that every step obeys gravity, and that the trajectory continues the
    object's previous window (defense 1 below);
  - where the requester's object has a track, that every step lies inside
    the tube around it (defense 2 below).

  Each check is arithmetic on ciphertext followed by the secure comparison.
  B learns only pass or fail.
- **After the window:** a requester with a public track opens the window; a
  requester without one answers challenges to its alerts (defense 3 below).

### What it costs

Measured with Microsoft SEAL 4.1.1 on one core of the shared workstation<sup>[13](#r13)</sup>:

| Step | R′ | A: encrypt and upload, per object-day | B: compute, per pair-day | B: response, per pair-day | A: decrypt, per pair-day |
| --- | ---: | ---: | ---: | ---: | ---: |
| 1 s | 9.2 km | 179 ms, 34.6 MB | 169 ms | 8.7 MB | 163 ms |
| 10 s | 77.7 km | 22 ms, 4.2 MB | 21 ms | 1.0 MB | 20 ms |

- **Correctness.** Over 86,400 one-second steps for eight pairs, every step
  matched the plaintext distance.
- **Detection.** Each designed 2 km encounter raised three to six alert steps.
  A control pair 60 km apart raised none at 1 s steps.
- **Scale.** One private object screened against 1,000 objects for three days
  at 1 s steps costs B about 510 core-seconds and 26 GB of responses. Private
  screening suits operator-to-operator subsets; the open screen covers the
  catalog.
- **Scope.** The measurement had A encrypt |a|² as a fourth quantity, and it
  covers the screening arithmetic only. B's squaring of |a|², the checks of
  defenses 1 and 2, the secure comparison and noise flooding come on top.
  The squaring and the physics check run once per submission, shared by all
  of B's objects. The physics check multiplies ciphertexts to depth 3 (4 with
  J2), which needs larger encryption parameters than the screen.

### What the result reveals

The intended output is the set of steps at which B is within R′, and nothing
more.

**A naive design leaks the distance.**
- The obvious way to hide the distance is to multiply each step's value by a
  fresh random factor and let A read only its sign. One step's value then
  bounds the distance only loosely.
- Neighbouring steps, however, have almost the same distance. Intersecting
  the bounds of 64 neighbouring steps, A recovered the distance within a
  factor of 2 at 87 % of steps (1 s grid). That is enough for range-only
  orbit determination of B's object.
- The output must therefore be one bit per step: B adds a random additive
  mask, and A and B run a two-party secure comparison that reveals only the
  sign, to both of them<sup>[12](#r12)</sup>.
- B's response must also be noise-flooded, so that its ciphertext noise
  carries nothing about b.

**Real conjunctions reveal what safety requires.** Each alert places the
other object within R′ of a known object at a known time. In this paper's
3-day SGP4 screen, 2,532 objects outside Starlink came within 5 km of a
Starlink satellite:
- 1,304 of them did so three or more times;
- 668 did so ten or more times.

Kuiper, OneWeb and Qianfan had 857, 452 and 571 such objects, mostly with one
to four conjunctions each. A private object in a crowded shell therefore
gives its densest neighbour enough position fixes for a coarse orbit within
days. Private screening hides an orbit from operators it does not approach.
It cannot hide it from those it does.

### Orbit guessing

The probing attack:
- **The query.** A requester submits invented trajectories, rather than its
  real object, to find B's object. Each step then answers whether B is
  within R′ of a chosen point at a chosen time.
- **A chosen radius.** If A supplied |a|², a false value would make each step
  test any radius around any point, which locates B without a prior. B
  therefore squares A's coordinates itself (step 2 above).
- **The cost.** Low Earth orbit (200–2,000 km altitude) holds about
  1.27 × 10¹² km³, and a 9.2 km ball is about 3,300 km³. A first hit with no
  prior takes about 3.9 × 10⁸ steps: 47,000 one-window queries. At the
  measured rates, B pays about 760 core-seconds and 39 GB of responses.
- **With a prior.** If the prober knows the shell (±25 km at 700 km), it
  needs about 1,200 queries, costing B 20 core-seconds and 1 GB.
- **After a first hit,** orbital dynamics narrow the search quickly.
- **In reverse.** The responder can probe the requester through its
  reactions. A disclosure request or a maneuver after an alert reveals
  proximity to an invented trajectory.

Unbounded querying is therefore cheap enough for a determined adversary. In
mutual screening both operators can attempt it: each is a requester for its
own object and a responder for the other's.

**Who enforces the defenses.**
- Every answer about an object comes from its owner's node, computed with
  its owner's plaintext trajectory. Nothing that could reveal the object
  exists anywhere else.
- The requester's software, modified or not, holds only its own data and
  results locked under its own key.
- The defenses below therefore run on the node of the party being probed.
  Open source does not weaken them; it lets each party check what its
  counterpart's node computes.
- A central service computing on everyone's encrypted orbits would reverse
  this, which is one more reason the protocol has none.

The defenses:

1. **Check the physics before answering.** This needs no track.
   - Each query names a registered object of the requester. Each object
     submits one trajectory, continued from window to window: a window's
     first k steps repeat the previous window's last k, and must match. When
     keys change per window, a key-switching key from the requester lets the
     responder compare them.
   - With the acceleration taken from second differences,
     sⱼ = aⱼ₊ₖ − 2aⱼ + aⱼ₋ₖ ≈ ä(tⱼ)(kΔt)², point-mass gravity requires

     $$
     \mathbf s_j \times \mathbf a_j = \mathbf 0, \qquad \lvert \mathbf s_j \rvert^2 \, \lvert \mathbf a_j \rvert^4 = \mu^2 (k\Delta t)^4 .
     $$

   - The differences need only shifts and sums of ciphertexts, and both
     conditions are polynomials. The responder evaluates them with
     ciphertext products and tests them against a tolerance τ in the secure
     comparison. Multiplying through by |a|⁷ keeps both polynomial with J2
     included, at degree 16 instead of 6.
   - Around a declared burn, the check admits the declared Δv, drawn from a
     per-object budget.
   - The responder learns only pass or fail. Point clouds, steered sweeps
     and blank steps fail before any answer is computed.
   - The tolerance bounds the thrust a fake orbit can hide to about τμ/|a|²:
     with τ = 10⁻⁴ in low Earth orbit, 0.8 mm/s², about 70 m/s per day.
     Rounding to whole metres, the truncation of the differences and forces
     outside the check set how small τ can be.
   - A requester with K registered objects then learns what K satellites on
     its claimed orbits would.
2. **Answer only near the requester's track.** This applies where the
   requester's object has a track the requester does not control.
   - **The track.** The public catalog's, or a certified private track: a
     tracker that already follows the object signs a copy of its track,
     encrypted under the requester's key. A certified track anchors a
     satellite missing from the public catalog without publishing it.
   - **The tube.** Under encryption, the responder tests that the submitted
     trajectory stays within D of the track. D is D₀ at the start and widens
     by Δv_max·(t − t_b) after a burn declared at t_b, so planned maneuvers
     pass. The responder learns only pass or fail.
   - **The mask.** Wherever the responder's object b is farther than D + R′
     from the track p, the answer is "far", whatever was submitted. An honest
     requester is within D of its track, so there |a − b| ≥ |b − p| − |a − p|
     > R′, and no honest answer changes. A masked step is a fresh encryption
     of a positive value, flooded like the rest, so the requester cannot tell
     it apart.
   - With a public track the responder computes the mask in plaintext, so it
     needs nothing from the requester. With a certified track it computes the
     mask under encryption, and a step is "close" only when both tests pass,
     in one secure comparison.
   - Probing with its real fleet, a requester reaches at most D + R′ around
     each of its satellites' tracks.
3. **Audit afterwards.**
   - **A requester with a public track opens the window.** Each window is
     encrypted under a fresh key, and the responder keeps the ciphertexts it
     answered. After the window has passed, the requester hands over that
     window's key. The responder decrypts exactly what it answered: past
     positions, far less sensitive than planned maneuvers. The ciphertext is
     the commitment: it cannot be swapped afterwards, and no separate hash is
     needed. The responder checks every step and orbital dynamics, and
     agreement with independent tracking of the declared object, using the
     reference-orbit comparisons of the screening paper's [section 7](fast-conjunction-assessment.md#7-uncertainty-and-probability-of-collision)<sup>[2](#r2)</sup>.
   - **A requester without one answers challenges.** Opening a window would
     expose its orbit. Instead, the responder may challenge any alert: the
     requester states, signed, its object's positions for a few minutes
     around it, which the responder already knows to within R′. The
     responder checks them against its own or a contracted tracker's
     observations, and an empty sky is a failure. This exposes a fake only
     where some tracker can see the stated position.
   - Queries, responses and alerts are digitally signed, so a failure is
     attributable evidence. Withholding the key or the positions counts as a
     failure.
4. **Stake and identity.** Only identities with stake or reputation may ask,
   and each object is registered, with stake, ahead of the windows it screens
   in. A failed check forfeits the stake and ends screening for that identity.
5. **Per-window budget.**
   - An identity may ask no more questions per window than it has
     registered objects.
   - That bounds what a requester willing to lose its identity can extract
     before an audit catches it: one window of questions, inside the tubes
     of its own fleet or along the orbits it registered.
6. **Laplace noise with a safety offset.** Before the comparison, the
   responder adds noise drawn from a Laplace distribution of scale b to each
   step's distance. The noise is truncated at ±s, and the test threshold is
   raised by s, so noise can add false alerts but never removes a real one.
   - **It hides the precise position.** Two placements of the hidden object
     whose distances to the probe differ by Δ change the odds of each answer
     by at most e^(Δ/b), and of k answers by e^(kΔ/b). This is differential
     privacy. With b = 250 m, ten answers barely separate positions 25 m
     apart.
   - **Finding the boundary is expensive.** Locating the alert boundary to
     within δ takes about (b/δ)² answers, more than the budget allows.
   - **It does not hide coarse location.** An object 1,000 km away is never
     "close". The noise complements the checks above; it does not replace
     them.
   - **Repeats don't help.** The noise is fixed per requester, object and
     step, so asking again does not average it away. Because of the
     truncation, the guarantee is approximate ((ε, δ)) differential privacy.
   - **Cost.** The alert radius grows by s. With s = 1.25 km at R = 5 km,
     there are about 1.6 times as many alerts to resolve.

Direct authenticated streams protect integrity and metadata. The ciphertexts
are protected by their owner's key either way.

With these defenses in place, a prober learns what its real objects' real
close approaches reveal. A requester whose objects have no track learns what
satellites on its claimed orbits would; a claimed orbit that no tracker
follows cannot be told from a real hidden satellite until one of its alerts is
challenged. If it is willing to lose its identity, a prober learns at most one
more window of answers, within its own fleet's tubes or along the orbits it
registered.

## 6 Keys

Records, manifests and notifications are signed, so their worth rests on the keys behind them. Every key an SDN node uses for a purpose is derived from the node's root key unless the operator configures another. The Adversarial Security paper turns such a key into public, continuous evidence of its own integrity<sup>[3](#r3)</sup>: addresses derived from the key on several blockchains hold value that anyone who steals the key can take, so value left in place shows the key is intact, and the amount bonded sets how much a verifier can rely on it.

## Data credits

- Source: USSPACECOM / 18th Space Defense Squadron, via Space-Track.org (https://www.space-track.org).
- SpaceX Starlink public ephemerides, https://api.starlink.com/public-files/ephemerides/ (SpaceX).
- CelesTrak supplemental GP, https://celestrak.org (T. S. Kelso).

## References

### R1

Koury, A. Evidence-Supported ASO Catalog. Space Data Network technical whitepaper 2.0, 11 October 2026. [Paper](evidence-supported-aso-catalog.md)

### R2

Koury, A. Fast All-vs-All Conjunction Screening. Space Data Network technical whitepaper 1.9, 11 October 2026. [Paper](fast-conjunction-assessment.md)

### R3

Koury, A. Persistent Adversarial Security. Space Data Network technical whitepaper. [Paper](adversarial-security.md)

### R4

Edgesource. Orbit accuracy experiments: frozen plans, run manifests, generated reports and data licenses, with a browser site that re-runs them. Commit 357d992d6c62daac4ccbb2fd230634ac292192b8. [Repository](https://github.com/DigitalArsenal/orbit-accuracy-experiments), [site](https://digitalarsenal.github.io/orbit-accuracy-experiments/)

### R5

Edgesource. Continuous, distributed, operator-first screening, 2026-10: work units, replication, the local harness and operator-first publication. Modules commit e1e6577acb0e54eb7f68ba909bc109b7b2a7a77d, `analysis/conjunction-assessment/docs/continuous-distributed-screening-2026-10.md`. Private repository, available on request ([tj@edgesource.com](mailto:tj@edgesource.com)).

### R6

Anderson, D. P. BOINC: a system for public-resource computing and storage. 5th IEEE/ACM International Workshop on Grid Computing, 4–10, 2004. [doi:10.1109/GRID.2004.14](https://doi.org/10.1109/GRID.2004.14)

### R7

Sarmenta, L. F. G. Sabotage-tolerance mechanisms for volunteer computing systems. Future Generation Computer Systems 18, 561–572, 2002. [doi:10.1016/S0167-739X(01)00077-2](https://doi.org/10.1016/S0167-739X(01)00077-2)

### R8

Haas, A., et al. Bringing the web up to speed with WebAssembly. PLDI 2017, 185–200. [doi:10.1145/3062341.3062363](https://doi.org/10.1145/3062341.3062363)

### R9

Taufer, M., et al. Homogeneous redundancy: a technique to ensure integrity of molecular simulation results using public computing. IPDPS 2005. [doi:10.1109/IPDPS.2005.247](https://doi.org/10.1109/IPDPS.2005.247)

### R10

Microsoft Research. Microsoft SEAL 4.1.1, homomorphic encryption library. [Repository](https://github.com/microsoft/SEAL)

### R11

Fan, J. and Vercauteren, F. Somewhat Practical Fully Homomorphic Encryption. IACR Cryptology ePrint Archive 2012/144. [Paper](https://eprint.iacr.org/2012/144)

### R12

Damgård, I., Geisler, M. and Krøigaard, M. Efficient and Secure Comparison for On-Line Auctions. ACISP 2007, LNCS 4586.

### R13

Edgesource. Private screening: protocol, SEAL benchmark, leakage and exposure measurements, exchange rules and defenses. Modules commit 22db2691620599403885cd1353677011e08f3d22. Files: `analysis/conjunction-assessment/docs/private-screening.md`, `analysis/conjunction-assessment/bench/private-screening`. Private repository, available on request ([tj@edgesource.com](mailto:tj@edgesource.com)).

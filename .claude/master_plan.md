# Space Data Network — Master Plan

**Owner:** DigitalArsenal.io, Inc.
**Last Updated:** 2026-02-06
**Status:** Draft v1.0

---

## Table of Contents

1. [Vision & Narrative](#1-vision--narrative)
2. [Ecosystem Map](#2-ecosystem-map)
3. [Website Unification Strategy](#3-website-unification-strategy)
4. [Commercialization Strategy](#4-commercialization-strategy)
5. [Pitch Deck Outline](#5-pitch-deck-outline)
6. [Funding & Grant Opportunities](#6-funding--grant-opportunities)
7. [Marketing Strategy](#7-marketing-strategy)
8. [Roadmap & Milestones](#8-roadmap--milestones)
9. [Risk Analysis & Mitigations](#9-risk-analysis--mitigations)
10. [Appendix: Repository Index](#10-appendix-repository-index)

---

## 1. Vision & Narrative

### The One-Liner

> **Space Data Network is the TCP/IP of space — an open, decentralized protocol for exchanging standardized space data, powered by an ecosystem of commercial tools that make space operations accessible to everyone.**

### The Problem

The space industry is fragmented by:

- **Proprietary silos** — Every operator, agency, and vendor uses incompatible formats and closed networks. Getting conjunction data from one entity to another requires bilateral agreements, email chains, or intermediary organizations.
- **Single points of failure** — Centralized clearinghouses (Space-Track.org, CelesTrak, 18th SDS) can go down, get defunded, or become geopolitically contested. When they do, the entire SSA ecosystem stalls.
- **Legacy data formats** — TLEs were designed for punch cards in the 1960s. VCMs and CDMs are exchanged as flat text files. There's no modern, type-safe, high-performance serialization standard.
- **No marketplace** — An operator with a high-fidelity radar wants to sell observations to a satellite operator who needs them. Today there's no trustless, automated way to do this. Every transaction requires lawyers and contracts.
- **No accessible tooling** — Orbital mechanics software costs $50K–$500K/year (STK, FreeFlyer, GMAT). Hardware-in-the-loop spacecraft simulation requires million-dollar facilities.

### The Solution

A vertically-integrated, open-core ecosystem:

| Layer | Open Source (Free) | Commercial (Revenue) |
|-------|-------------------|---------------------|
| **Standards** | Space Data Standards (127 FlatBuffers schemas) | — |
| **Serialization** | flatc-wasm (FlatBuffers compiler in WASM) | — |
| **Query** | FlatSQL (SQL over FlatBuffers) | — |
| **Identity & Crypto** | hd-wallet-wasm (HD wallets, signing) | — |
| **Network** | Space Data Network (P2P protocol) | Data Marketplace (transaction fees) |
| **Simulation** | Tudat-WASM, Basilisk-WASM | — |
| **Visualization** | CesiumJS (base) | OrbPro2/3 (licensed product) |
| **AI/NLP** | — | OrbPro2-MCP (NLP globe control) |
| **Modeling & Sim** | — | OrbPro2-ModSim (combat/mission sim) |
| **Platform** | — | SpaceAware.io (SaaS accounts, dashboards) |

### Why Now

1. **Regulatory momentum** — The UN COPUOS Long-term Sustainability Guidelines, FCC orbital debris rules, and proposed Space Traffic Management frameworks all require better data sharing.
2. **Commercial SSA explosion** — LeoLabs, ExoAnalytic, Kayhan Space, Slingshot Aerospace are proving there's a market for SSA data and tools.
3. **WASM maturity** — WebAssembly now enables full astrodynamics simulation in the browser. No installs, no license servers, no vendor lock-in.
4. **Decentralization technology** — libp2p, IPFS, and FlatBuffers are production-ready for building trustless data networks.
5. **AI integration** — MCP (Model Context Protocol) enables natural-language interfaces for space operations, dramatically lowering the barrier to entry.

---

## 2. Ecosystem Map

```
┌─────────────────────────────────────────────────────────────────────┐
│                     USER-FACING PRODUCTS                            │
│                                                                     │
│  ┌─────────────┐  ┌──────────────┐  ┌──────────────────────────┐   │
│  │ SpaceAware  │  │   OrbPro2    │  │     OrbPro2-MCP          │   │
│  │   .io       │  │   Desktop    │  │  (NLP Globe Control)     │   │
│  │  (SaaS)     │  │  (Licensed)  │  │  (Browser AI + CesiumJS) │   │
│  └──────┬──────┘  └──────┬───────┘  └───────────┬──────────────┘   │
│         │                │                       │                   │
│  ┌──────┴────────────────┴───────────────────────┴──────────────┐   │
│  │              OrbPro2-ModSim (18 WASM plugins)                │   │
│  │         608 entity types · combat sim · astrodynamics        │   │
│  └──────────────────────────┬───────────────────────────────────┘   │
│                             │                                       │
├─────────────────────────────┼───────────────────────────────────────┤
│                     OPEN INFRASTRUCTURE                             │
│                             │                                       │
│  ┌──────────────────────────┴───────────────────────────────────┐   │
│  │              Space Data Network (P2P Protocol)               │   │
│  │   libp2p · GossipSub · DHT · Circuit Relay · Marketplace    │   │
│  └───┬──────────┬────────────┬──────────────┬───────────────────┘   │
│      │          │            │              │                       │
│  ┌───┴───┐  ┌──┴─────┐  ┌──┴───────┐  ┌──┴──────────────┐        │
│  │FlatSQL│  │flatc   │  │hd-wallet │  │Space Data       │        │
│  │(Query)│  │-wasm   │  │-wasm     │  │Standards (127)  │        │
│  └───────┘  │(Serial)│  │(Identity)│  └─────────────────┘        │
│             └────────┘  └──────────┘                              │
│                                                                     │
│  ┌──────────────────────────────────────────────────────────────┐   │
│  │  Simulation Engines: Tudat-WASM + Basilisk-WASM (310+ cls)  │   │
│  └──────────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────┘
```

### Repository-to-Product Mapping

| Repository | Role | License | Revenue Model |
|---|---|---|---|
| `space-data-network` | P2P protocol, marketplace | MIT | Transaction fees |
| `spacedatastandards.org` | Schema definitions + website | Apache 2.0 | None (drives adoption) |
| `flatbuffers/wasm` | Serialization engine | Apache 2.0 | None (drives adoption) |
| `flatsql` | Query engine | Apache 2.0 | None (drives adoption) |
| `hd-wallet-wasm` | Identity & crypto | Apache 2.0 | None (drives adoption) |
| `tudat-wasm` | Astrodynamics engine | BSD 3-Clause | None (drives adoption) |
| `basilisk` | Spacecraft simulation | ISC | None (drives adoption) |
| `OrbPro` | 3D visualization platform | Proprietary | License sales |
| `OrbPro2-MCP` | AI-powered globe control | Proprietary | Bundled with OrbPro |
| `OrbPro2-ModSim` | Modeling & simulation | Proprietary | Bundled with OrbPro |
| `WEBGPU_OrbPro3` | Next-gen rendering engine | Proprietary | Future OrbPro version |
| `spaceaware.io` | SaaS platform (TBD) | Proprietary | Subscriptions |

---

## 3. Website Unification Strategy

### Current State

Each project has its own website with independent styling, navigation, and messaging. A visitor landing on one site has no idea the others exist.

### Target State

All sites share a unified visual identity and cross-link as parts of one ecosystem, while each site remains focused on its specific audience and purpose.

### Unified Design System

**Shared Elements Across All Sites:**
- Common header/nav bar with ecosystem dropdown menu
- Consistent color palette (dark theme primary, space-inspired)
- Shared footer with links to all ecosystem projects
- "Part of the Space Data Network ecosystem" badge
- Consistent typography (monospace for technical, sans-serif for marketing)

### Per-Site Strategy

#### A. **spacedatanetwork.io** (Hub Site — Create New or Rebrand Existing)
- **Audience:** Everyone — first-time visitors, investors, developers, operators
- **Purpose:** Ecosystem landing page and routing
- **Content:**
  - 60-second animated explainer of the full ecosystem
  - "Choose your path" cards: Developer / Operator / Investor / Researcher
  - Live network stats (peer count, messages/day, schemas in use)
  - Links to all sub-sites
  - Blog/news feed

#### B. **spacedatastandards.org** (Standards Body Site)
- **Audience:** Standards committee members, data engineers, schema contributors
- **Purpose:** Schema reference and governance
- **Content Refinements:**
  - Sharpen the hero: "The FlatBuffers standard for space data — 127 schemas, 13 languages, zero-copy performance"
  - Add "Adopted by" logos section (even if it's your own projects initially)
  - Interactive schema explorer (already exists — polish it)
  - Governance page: How to propose new schemas, versioning policy
  - Migration guides: "Convert your TLEs/VCMs/CDMs to SDS"

#### C. **digitalarsenal.io** (Company Site)
- **Audience:** Potential customers, partners, investors
- **Purpose:** Company credibility and commercial offerings
- **Content Refinements:**
  - Position as "The company behind Space Data Network"
  - Products page: OrbPro2, SpaceAware.io, OrbPro2-ModSim
  - Open source portfolio page
  - Team / About / Contact
  - Case studies and testimonials

#### D. **spaceaware.io** (SaaS Platform — To Be Created)
- **Audience:** Satellite operators, SSA analysts, defense/intel users
- **Purpose:** Commercial platform for space situational awareness
- **Content:**
  - Product features and demo video
  - Pricing tiers (Free / Pro / Enterprise)
  - Account creation and login
  - Dashboard screenshots/live demo
  - Integration docs (how SpaceAware uses SDN under the hood)

#### E. **OrbPro Product Pages** (Subsection of digitalarsenal.io or standalone)
- **Audience:** GIS developers, defense contractors, space software teams
- **Purpose:** Sell OrbPro2 licenses
- **Content:**
  - Feature comparison vs. STK, FreeFlyer, GMAT
  - Pricing (see commercialization section)
  - Sandcastle gallery of demos
  - API documentation
  - Plugin marketplace preview

#### F. **GitHub Pages for Open-Source Repos**
- Each repo (SDN, flatbuffers, flatsql, hd-wallet-wasm, tudat-wasm, basilisk) keeps its GitHub Pages docs
- Add unified header bar linking back to spacedatanetwork.io
- Consistent "Part of the SDN ecosystem" branding

### Implementation Priority

1. Add unified nav/footer component to all existing sites (1-2 weeks)
2. Create spacedatanetwork.io hub landing page (2-3 weeks)
3. Design and launch spaceaware.io MVP (4-6 weeks)
4. Polish OrbPro product pages (2-3 weeks)
5. Update digitalarsenal.io as company hub (1-2 weeks)

---

## 4. Commercialization Strategy

### Revenue Streams

#### Stream 1: OrbPro2 Licenses ($$$)

**Product Tiers:**

| Tier | Price | Includes |
|------|-------|---------|
| **OrbPro2 Community** | Free | CesiumJS base + basic orbit visualization |
| **OrbPro2 Professional** | $2,500/year/seat | SGP4 WASM plugin, sensor modeling, viewshed analysis, access analysis, terrain pinning, draggable entities |
| **OrbPro2 Enterprise** | $10,000/year/seat | All Professional + ModSim plugins (608 entity types), combat simulation, MCP NLP interface, priority support, custom plugins |
| **OrbPro2 Government** | $25,000/year/seat | All Enterprise + FIPS 140-3 crypto, classified network deployment support, ITAR-compliant builds, on-prem licensing |

**Volume Discounts:**
- 5-10 seats: 15% off
- 11-50 seats: 25% off
- 50+ seats: Custom enterprise agreement

**Plugin Add-Ons (a la carte):**
- SGP4 Propagation Plugin: $500/year
- Sensor Modeling Plugin: $500/year
- Viewshed Analysis Plugin: $500/year
- Combat Simulation Plugin: $1,500/year
- MCP NLP Control Plugin: $1,000/year

#### Stream 2: SpaceAware.io Subscriptions ($$)

**Account Tiers:**

| Tier | Price | Features |
|------|-------|---------|
| **Free** | $0 | View public catalog (GP/TLE), basic 3D globe, 10 tracked objects |
| **Starter** | $49/month | 100 tracked objects, conjunction alerts, ephemeris history, API access (1K calls/day) |
| **Professional** | $199/month | Unlimited tracked objects, high-fidelity propagation (Tudat/Basilisk WASM), custom sensor FOV analysis, API (50K calls/day), data export |
| **Team** | $499/month | 5 seats, shared workspaces, collision avoidance workflows, maneuver planning, integration webhooks |
| **Enterprise** | Custom | SSO/SAML, SLA, dedicated SDN node, custom data feeds, on-prem option |

#### Stream 3: Data Marketplace Transaction Fees ($$$)

**Built into the SDN protocol's storefront system:**

| Transaction Type | Fee Structure |
|---|---|
| **Data Sales** | 5% platform fee on each transaction |
| **Plugin Sales** | 15% platform fee (plugin marketplace) |
| **Subscription Data Feeds** | 5% of recurring revenue |
| **Free/Open Data** | $0 (always free, drives network effects) |

**Marketplace Categories:**
- Premium ephemeris data (high-precision observations)
- Conjunction analysis reports
- Historical orbital data archives
- Atmospheric density models
- Real-time RF monitoring data
- Debris tracking observations
- Custom propagation algorithms (plugins)
- Sensor tasking results

#### Stream 4: NFT-Based Asset Timeshares ($$$ — Longer Term)

**Concept:** Tokenize time slots and capabilities on on-orbit assets, ground stations, and data centers.

**Use Cases:**

| Asset Type | NFT Represents | Example |
|---|---|---|
| **Satellite observation time** | 1-hour imaging pass over a region | "10 minutes of optical tracking from LEO sat #42 on 2026-03-15" |
| **Ground station access** | Antenna time for uplink/downlink | "S-band pass from Canberra DSN, 15-min slot" |
| **Compute on edge/space** | Processing time on orbital compute nodes | "1 GPU-hour on orbital data center" |
| **Spectrum rights** | Temporary frequency allocation | "X-band 8.2 GHz, 36 MHz bandwidth, 2-hour window" |
| **Data center colocation** | Rack space in SDN-connected facility | "1U rack-month in Ashburn, SDN-peered" |

**Implementation:**
- Use hd-wallet-wasm for key management and signing
- Mint NFTs on Solana (low fees, fast finality) or Ethereum L2
- Smart contracts enforce time-slot ownership and access control
- SDN marketplace handles discovery and payment
- Atomic swaps between data credits and NFTs

**Revenue:** 2.5% minting fee + 1% secondary market royalty

#### Stream 5: Consulting & Integration Services ($$)

- SDN node deployment and configuration
- Custom plugin development for OrbPro2
- Space data pipeline architecture
- Migration from legacy formats to Space Data Standards
- Training and workshops

**Rates:** $250-400/hour, or fixed-price project engagements

### Revenue Projections (Conservative Estimates)

| Year | OrbPro2 Licenses | SpaceAware.io | Marketplace Fees | NFTs | Services | Total |
|------|-----------------|---------------|-------------------|------|----------|-------|
| **Y1** | $150K | $50K | $25K | $0 | $75K | **$300K** |
| **Y2** | $500K | $200K | $150K | $50K | $150K | **$1.05M** |
| **Y3** | $1.2M | $600K | $500K | $200K | $300K | **$2.8M** |
| **Y4** | $2.5M | $1.5M | $1.5M | $500K | $500K | **$6.5M** |
| **Y5** | $5M | $3M | $4M | $1.5M | $750K | **$14.25M** |

---

## 5. Pitch Deck Outline

### Slide Structure (12 slides, 3-minute pitch)

**Slide 1 — Title**
> Space Data Network: The Open Protocol for Space Data Exchange
> DigitalArsenal.io, Inc.

**Slide 2 — The Problem**
- 10,000+ active satellites, 40,000+ tracked objects, growing 30% annually
- Data exchange relies on email, FTP, and 1960s-era formats (TLE)
- $50K-$500K/year for orbital mechanics software
- No standardized marketplace for space data
- Starlink alone = half of all active satellites, yet sharing data is ad-hoc

**Slide 3 — The Solution**
- Decentralized P2P protocol (like BitTorrent for space data)
- 127 standardized schemas (like HTTP content types for space)
- Everything runs in the browser via WebAssembly
- Open infrastructure, commercial products on top

**Slide 4 — How It Works (Architecture)**
- [Ecosystem diagram from Section 2]
- Open standards layer → Open network layer → Commercial products
- "We built TCP/IP for space, and we're selling Cisco routers"

**Slide 5 — The Open Source Moat**
- Space Data Standards: 127 schemas, 13 languages, adopted as foundation
- FlatBuffers WASM: Zero-dependency serialization in browser
- FlatSQL: SQL queries over binary data at 580K ops/sec
- Tudat + Basilisk WASM: Full astrodynamics simulation in browser (world first)
- HD-Wallet-WASM: Cryptographic identity and blockchain integration
- **6 years of R&D, 100K+ lines of code, impossible to replicate quickly**

**Slide 6 — Commercial Products**
- **OrbPro2**: CesiumJS-based visualization platform with WASM plugins — competes with $500K/yr STK at 1/20th the cost
- **SpaceAware.io**: SaaS platform for SSA — subscription-based dashboards, alerts, analysis
- **Data Marketplace**: Built into the protocol — 5% transaction fees on a growing $2B+ SSA market

**Slide 7 — Demo / Screenshots**
- OrbPro2 3D visualization with sensor modeling
- Natural language control via MCP ("Show me all Starlink satellites over Europe")
- Basilisk spacecraft simulation running in browser
- Data marketplace storefront

**Slide 8 — Market Size**
- Space Situational Awareness market: $1.5B (2025) → $3.6B (2030) — 19% CAGR
- Earth Observation data market: $7.5B (2025) → $14.5B (2030)
- Space simulation software: $2.1B (2025) → $4.8B (2030)
- Our SAM (Serviceable Addressable Market): $800M by 2030

**Slide 9 — Business Model**
- Revenue mix: License (35%) + SaaS (25%) + Marketplace (25%) + Services (15%)
- Gross margins: 85%+ (software)
- Open source drives adoption → adoption drives commercial attach rate
- Network effects: More nodes = more data = more marketplace value

**Slide 10 — Traction & Validation**
- [Insert current metrics: GitHub stars, npm downloads, node count]
- Space Data Standards used by [list any adopters]
- FlatBuffers WASM: [npm download count] downloads
- OrbPro: [customer/pilot count]
- Government interest: [any LOIs, pilots, or conversations]

**Slide 11 — Team**
- [Founder/team bios]
- Advisory board (if any)
- Key technical accomplishments (first WASM astrodynamics simulation, etc.)

**Slide 12 — The Ask**
- Raising: $[X]M Seed / Series A
- Use of funds: 40% Engineering, 25% GTM, 20% Operations, 15% Reserve
- Key milestones for next 18 months
- Contact info

### Supplementary Slides (Appendix)

- **Competitive Landscape**: STK vs. FreeFlyer vs. GMAT vs. OrbPro2 feature matrix
- **Technical Deep Dive**: Architecture, performance benchmarks, security model
- **Customer Pipeline**: LOIs, pilots, conversations
- **IP Portfolio**: List of key innovations and potential patents
- **NFT Asset Tokenization**: Detailed vision for satellite timeshare marketplace

---

## 6. Funding & Grant Opportunities

### Government Grants & Programs

#### A. Space-Specific

| Program | Agency | Relevance | Amount | Deadline |
|---------|--------|-----------|--------|----------|
| **SBIR/STTR Phase I** | NASA | Space data standards, SSA tools | $150K | Rolling (multiple solicitations/year) |
| **SBIR/STTR Phase II** | NASA | Follow-on from Phase I | $750K | By invitation |
| **AFRL SBIR** | Air Force / Space Force | SSA, space domain awareness | $150K-$1.5M | Rolling |
| **SpaceWERX SBIR/STTR** | Space Force | Space technology commercialization | $50K-$1.5M | Rolling |
| **SpaceWERX Orbital Prime** | Space Force | On-orbit servicing data exchange | Varies | Periodic |
| **DARPA** | DoD | Decentralized space networks, autonomous SSA | $500K-$5M | Topic-dependent |
| **SDA (Space Development Agency)** | DoD | Proliferated LEO data mesh | Varies | BAAs |
| **NOAA** | Commerce | Space weather data standards | $100K-$500K | Annual |
| **NSF Convergence Accelerator** | NSF | Open-source infrastructure for national needs | $750K (Phase 1), $5M (Phase 2) | Annual |
| **NIST** | Commerce | Data standards development | $50K-$300K | Varies |

#### B. Technology-General

| Program | Agency | Relevance | Amount |
|---------|--------|-----------|--------|
| **NSF POSE** | NSF | Pathways to Open-Source Ecosystems | $300K-$1.5M |
| **ARPA-H / ARPA-E** | DoE/HHS | Novel use of decentralized data networks | $1M-$10M |
| **NTIA Public Wireless** | Commerce | Open-source network infrastructure | Varies |

### Venture Capital — Space-Focused

| Firm | Focus | Stage | Notable Investments |
|------|-------|-------|-------------------|
| **Space Capital** | Space infrastructure | Seed-B | Kayhan Space, LeoLabs, Privateer |
| **Seraphim Space** | Space tech | Seed-B | D-Orbit, Spire, HawkEye 360 |
| **Promus Ventures** | Space & defense | Seed-A | |
| **Type One Ventures** | Deep tech / space | Pre-Seed-A | |
| **Stellar Ventures** | Space startups | Seed-A | |
| **Lockheed Martin Ventures** | Defense/space tech | Series A+ | |
| **Boeing HorizonX** | Aerospace tech | Series A+ | |
| **Airbus Ventures** | Aerospace innovation | Series A+ | |
| **In-Q-Tel** | Intelligence community tech | Any stage | Critical for gov adoption |
| **USIT (US Innovative Technology Fund)** | National security tech | Seed-B | |

### Venture Capital — Deep Tech / Open Source

| Firm | Focus | Stage | Notable Investments |
|------|-------|-------|-------------------|
| **a16z** | Open source / crypto / infra | Seed-Growth | Protocol Labs (IPFS) |
| **Sequoia** | Infrastructure / platform | Seed-Growth | |
| **OSS Capital** | Open-source-first companies | Seed | |
| **Heavybit** | Developer tools | Seed-A | |
| **Boldstart Ventures** | Developer-first startups | Pre-Seed-Seed | |
| **Gradient Ventures** (Google) | AI + infrastructure | Seed-A | |
| **Multicoin Capital** | Crypto / decentralized protocols | Seed-B | |
| **Polychain Capital** | Decentralized infrastructure | Seed-B | |

### Strategic Partners & Accelerators

| Organization | Type | Value |
|---|---|---|
| **Techstars Allied Space** | Accelerator | $120K + mentorship + USAF/Space Force connections |
| **Starburst Aerospace** | Accelerator | Defense/space customer intros |
| **AFWERX** | DoD innovation | Direct path to Space Force contracts |
| **Plug and Play Space** | Accelerator | Corporate partner connections |
| **Creative Destruction Lab (Space)** | Accelerator | Canadian space ecosystem |
| **ESA BIC** | Incubator | European Space Agency business incubation |
| **Y Combinator** | Accelerator | General — but has funded space companies |
| **Microsoft for Startups** | Program | Azure credits, AI tools, go-to-market |
| **AWS Space** | Program | Cloud credits, ground station partnerships |
| **Google for Startups** | Program | GCP credits, AI/ML resources |

### Non-Dilutive Funding Strategies

1. **NASA SBIR Fast-Track**: Submit Phase I, immediately apply for Phase II bridge
2. **SpaceWERX Pitch Days**: Rapid contracting (days, not months)
3. **CDAO (Chief Digital & AI Office)**: AI/data standards adoption across DoD
4. **NIST Standards Grants**: Funding for standards development and adoption
5. **Open Source Security Foundation (OpenSSF)**: Grants for security-critical open-source infrastructure

---

## 7. Marketing Strategy

### Brand Positioning

**Tagline Options:**
- "The Open Protocol for Space Data"
- "Space Awareness for Everyone"
- "From Orbit to Insight — Open, Decentralized, Unstoppable"

**Key Messages:**
1. **For Developers**: "Build space applications with zero-copy data, browser-native simulation, and a global P2P network — all open source."
2. **For Operators**: "Real-time conjunction alerts, high-fidelity propagation, and a marketplace for the data you need — at 1/20th the cost of legacy tools."
3. **For Investors**: "The open-core company building the data layer for the $400B space economy."
4. **For Government**: "Resilient, decentralized SSA infrastructure that can't be denied, degraded, or destroyed."

### Content Strategy — Claude Teams Agentic Approach

#### Video Content Pipeline

Use Claude Teams to script, storyboard, and produce content at scale:

**1. Explainer Videos (4-6 per quarter)**
- "What is Space Data Network?" (2-min animated explainer)
- "How SDN Replaces Email-Based Conjunction Warnings" (3-min demo)
- "OrbPro2: STK-Level Analysis in Your Browser" (5-min product demo)
- "Building a Satellite Tracker in 10 Minutes with SDN" (tutorial)

**Production Workflow with Claude Teams:**
```
1. Claude writes script + shot list from product docs
2. Human reviews/approves script
3. Claude generates Manim/Motion Canvas animation code
4. Claude creates voiceover script optimized for ElevenLabs/TTS
5. Human records or generates voiceover
6. Claude writes YouTube description, tags, chapters, social posts
7. Human publishes, Claude drafts follow-up engagement responses
```

**2. Demo/Tutorial Videos (2 per week)**
- Screen recordings with Claude-generated narration scripts
- "Build X with SDN" series (satellite tracker, conjunction alerter, data marketplace listing)
- Use OrbPro2 Sandcastle gallery as demo content

**3. Conference Talk Prep**
- Claude drafts presentations from master plan content
- Generates speaker notes and Q&A preparation
- Creates one-pagers and leave-behinds

#### Written Content Pipeline

**LinkedIn Articles (1-2 per week)**

Claude Teams workflow:
```
1. Provide Claude with topic + key data points
2. Claude drafts 800-1200 word article with:
   - Hook (surprising stat or question)
   - Problem framing
   - Solution narrative (naturally incorporating SDN)
   - Call to action
3. Human reviews, adds personal anecdotes
4. Claude generates 5 LinkedIn post variations (different hooks)
5. Schedule across the week with different angles
```

**Article Topic Calendar:**
- Week 1: "Why TLEs Need to Die" (problem awareness)
- Week 2: "I Ran Spacecraft Simulation in My Browser" (technical wow)
- Week 3: "The $500K Software That Should Be Free" (market disruption)
- Week 4: "Decentralizing Space Data: Lessons from BitTorrent" (technical vision)
- Week 5: "127 Standards, 13 Languages, Zero Vendor Lock-in" (standards story)
- Week 6: "NFTs for Satellite Time — Crazy or Inevitable?" (future vision)
- Repeat themes with fresh angles

**Blog Posts (2-4 per month)**
- Technical deep dives for developer audience
- Product announcements and updates
- Case studies (even hypothetical initially)
- "State of the Network" monthly reports

#### Social Media Strategy

**LinkedIn (Primary Channel)**
- Target: Space industry professionals, defense procurement, VC
- Post frequency: 5x/week
- Mix: 40% thought leadership, 30% product demos, 20% industry commentary, 10% team/culture
- Use Claude to draft all posts, schedule with Buffer/Hootsuite

**Twitter/X (Secondary)**
- Target: Developers, crypto/web3 community, space enthusiasts
- Post frequency: 3-5x/day (threads + quick updates)
- Live-tweet conference attendance
- Engage with space industry accounts

**YouTube (Demos & Tutorials)**
- Post frequency: 2x/week
- Optimize thumbnails and titles with Claude
- Build playlist structure: Tutorials / Product Demos / Talks / Explainers

**GitHub (Community Building)**
- Maintain active discussions and issue engagement
- Monthly "What's New" releases with changelogs
- Contributor spotlights
- "Good First Issue" labels for onboarding

#### Paid Advertising

**Phase 1 (Months 1-6): Awareness**
- LinkedIn Ads targeting: Space industry titles (SSA analyst, satellite operator, astrodynamics engineer)
- Google Ads: Keywords around "orbital mechanics software", "SSA tools", "conjunction assessment"
- Budget: $2K-5K/month
- Goal: Drive traffic to spacedatanetwork.io, collect email leads

**Phase 2 (Months 6-12): Conversion**
- Retargeting website visitors with OrbPro2 trial offers
- SpaceAware.io free tier signup campaigns
- Webinar promotion ads
- Budget: $5K-10K/month

**Phase 3 (Year 2+): Scale**
- Conference sponsorships (AMOS, Space Symposium, SmallSat, IAC)
- Print ads in SpaceNews, Via Satellite
- Podcast sponsorships (Space Café, T-Minus, Main Engine Cut Off)
- Budget: $15K-25K/month

#### Print & Conference Materials

**Collateral (Claude-Generated, Designer-Polished):**
- 2-page product briefs for each product
- Technical white papers (SDN protocol, SDS schemas, OrbPro2 architecture)
- Case study templates
- Business cards with QR code to spacedatanetwork.io
- Conference booth banner and tablecloth designs
- Sticker/swag designs

**Key Conferences:**

| Conference | When | Why |
|---|---|---|
| **AMOS (Advanced Maui Optical & Space Surveillance)** | Sep | Premier SSA conference — decision makers |
| **Space Symposium** | Apr | Largest US space conference |
| **SmallSat** | Aug | Small satellite operators — our target market |
| **IAC (International Astronautical Congress)** | Oct | Global reach, standards adoption |
| **GEOINT** | Jun | Intelligence community — SpaceAware customers |
| **CES / Web Summit** | Jan/Nov | Tech press, general awareness |
| **FOSS4G** | Varies | Open-source geospatial community |
| **KubeCon** | Varies | P2P/infrastructure developer community |

#### Community Building

**Developer Relations:**
- Monthly virtual "SDN Office Hours" (Zoom/Discord)
- Hackathon sponsorship (sponsor space track at HackMIT, SpaceHacks, etc.)
- "SDN Ambassador" program for university researchers
- Open-source contributor incentive program (swag, recognition, bounties)

**Strategic Partnerships:**
- CesiumJS community cross-promotion
- IPFS/Protocol Labs ecosystem collaboration
- University partnerships (CU Boulder/AVS Lab for Basilisk, TU Delft for Tudat)
- Integrate with existing SSA tools (LeoLabs API, Space-Track, CelesTrak)

---

## 8. Roadmap & Milestones

### Phase 1: Foundation (Months 1-6)

**Objective:** Unified web presence, OrbPro2 launch, initial revenue

| Milestone | Target | Status |
|---|---|---|
| Unify website nav/footer across all sites | Month 1 | Not started |
| Launch spacedatanetwork.io hub site | Month 2 | Not started |
| OrbPro2 Professional tier pricing page + license system | Month 2 | Not started |
| First 3 OrbPro2 paying customers | Month 3 | Not started |
| Submit NASA SBIR Phase I proposal | Month 3 | Not started |
| SpaceAware.io landing page + waitlist | Month 4 | Not started |
| Launch data marketplace beta (on SDN testnet) | Month 5 | Not started |
| First LinkedIn article series (6 articles) | Month 2-4 | Not started |
| 10 SDN full nodes running | Month 6 | Not started |

### Phase 2: Growth (Months 6-12)

**Objective:** SpaceAware.io launch, marketplace traction, grant funding secured

| Milestone | Target |
|---|---|
| SpaceAware.io MVP launch (Free + Starter tiers) | Month 7 |
| 50 SpaceAware.io accounts | Month 8 |
| First data marketplace transaction | Month 8 |
| SBIR Phase I award (or resubmit) | Month 9 |
| 10 OrbPro2 paying customers | Month 9 |
| First conference talk (AMOS or SmallSat) | Month 8-9 |
| 50 SDN full nodes | Month 10 |
| First YouTube tutorial series (5 videos) | Month 8 |
| $100K cumulative revenue | Month 12 |

### Phase 3: Scale (Months 12-24)

**Objective:** Series Seed/A raise, enterprise customers, marketplace flywheel

| Milestone | Target |
|---|---|
| Raise $1.5-3M Seed round | Month 14 |
| SpaceAware.io Professional + Team tiers | Month 13 |
| 500 SpaceAware.io accounts | Month 15 |
| OrbPro3 (WebGPU) beta launch | Month 16 |
| NFT asset tokenization pilot (ground station time) | Month 18 |
| First enterprise customer ($50K+ ACV) | Month 15 |
| 200 SDN full nodes globally | Month 18 |
| SBIR Phase II award | Month 18 |
| $1M ARR | Month 24 |

### Phase 4: Dominance (Months 24-48)

**Objective:** Market leadership, protocol standard adoption, international expansion

| Milestone | Target |
|---|---|
| SDN protocol submitted to CCSDS or ITU for standardization | Month 30 |
| Space Data Standards adopted by 3+ government agencies | Month 30 |
| 1,000+ SDN nodes globally | Month 30 |
| OrbPro2 competitive with STK in government evaluations | Month 36 |
| NFT marketplace for satellite time operational | Month 36 |
| $5M ARR | Month 36 |
| Series A raise ($8-15M) | Month 36 |
| International office (ESA/JAXA partner region) | Month 48 |
| $15M ARR | Month 48 |

---

## 9. Risk Analysis & Mitigations

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| **CesiumJS changes license** | Low | High | OrbPro2 is a deep fork; can maintain independently. WebGPU OrbPro3 reduces dependency. |
| **Government builds competing open standard** | Medium | High | Stay ahead technically. Offer to contribute SDS to CCSDS. Be the reference implementation. |
| **ITAR/export control on simulation tools** | Medium | Medium | Tudat (Dutch) and Basilisk (university) have academic exceptions. Browser-WASM distribution is software, not hardware. Legal review needed. |
| **Crypto/NFT regulatory risk** | Medium | Medium | NFT features are optional layer. Support Stripe/fiat payments as primary. |
| **Slow enterprise sales cycle** | High | Medium | Focus on self-serve (SpaceAware.io) for revenue while building enterprise pipeline. Government grants bridge the gap. |
| **Open-source competitors** | Medium | Medium | 6+ years of R&D is hard to replicate. Community + ecosystem effects compound. Stay 2 years ahead. |
| **Key person risk** | High | High | Document everything. Build team. Use Claude Teams to capture institutional knowledge. |
| **Decentralization adoption resistance** | Medium | Medium | Position SDN as optional enhancement, not replacement. Support centralized deployment too. |

---

## 10. Appendix: Repository Index

| # | Repository | Path | Description | License |
|---|-----------|------|-------------|---------|
| 1 | space-data-network | `./` | P2P protocol, SDN server (Go), JS SDK, Desktop, WebUI | MIT |
| 2 | flatbuffers/wasm | `../flatbuffers/wasm` | FlatBuffers compiler in WASM, 13-lang codegen, encryption | Apache 2.0 |
| 3 | flatsql | `../flatsql` | SQL query engine over FlatBuffers via SQLite virtual tables | Apache 2.0 |
| 4 | hd-wallet-wasm | `../hd-wallet-wasm` | HD wallet, BIP-32/39/44, 50+ chains, FIPS 140-3 | Apache 2.0 |
| 5 | spacedatastandards.org | `../spacedatastandards.org` | 127 FlatBuffers schemas for space data, Svelte website | Apache 2.0 |
| 6 | tudat-wasm | `../tudat-wasm` | TU Delft astrodynamics toolbox compiled to WASM | BSD 3-Clause |
| 7 | basilisk | `../basilisk` | Basilisk spacecraft simulator compiled to WASM (310+ classes, 1757 tests) | ISC |
| 8 | OrbPro | `../OrbPro` | CesiumJS fork with orbital viz, sensor modeling, viewshed, access analysis | Proprietary |
| 9 | OrbPro2-MCP | `../OrbPro2-MCP` | In-browser LLM + MCP for natural language globe control | Proprietary |
| 10 | OrbPro2-ModSim | `../OrbPro2-ModSim` | 18 WASM plugins, 608 entity types, combat/mission simulation | Proprietary |
| 11 | WEBGPU_OrbPro3 | `../WEBGPU_OrbPro3` | Next-gen WebGPU CesiumJS rendering engine | Proprietary |
| 12 | spaceaware.io | `../spaceaware.io` | SaaS platform for space awareness (TO BE CREATED) | Proprietary |
| 13 | DigitalArsenal.io | `../DigitalArsenal.io` | Company website, Svelte + CesiumJS + Tailwind | Proprietary |

---

## Action Items — Immediate Next Steps

### This Week
- [ ] Review and refine this master plan
- [ ] Decide on fundraising strategy (bootstrap vs. grant-first vs. VC-first)
- [ ] Identify first 3 target OrbPro2 customers to approach

### This Month
- [ ] Build unified nav component for all websites
- [ ] Create spacedatanetwork.io landing page
- [ ] Set up OrbPro2 licensing/payment system
- [ ] Draft first 3 LinkedIn articles
- [ ] Identify specific SBIR topics for next submission window

### This Quarter
- [ ] Launch OrbPro2 commercially
- [ ] SpaceAware.io landing page + waitlist
- [ ] Submit first SBIR proposal
- [ ] Attend or present at first conference
- [ ] Create explainer video
- [ ] Reach $25K revenue

---

*This is a living document. Update as strategy evolves.*

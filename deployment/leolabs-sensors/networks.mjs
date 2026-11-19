// FACTS ONLY, for every space surveillance and tracking network other than LeoLabs (LeoLabs is roster.mjs).
// Owner 2026-11-18: "We also need all other sensor networks with their frequencies as well."; "publish them all, same as leolabs";
// "Also, we need the in orbit sensors definitely". Source of every row: SSA-NETWORKS.md (research lane SSA-SITES, three passes)
// and the catalogs named in the row. Nothing here is computed except the degrees-minutes-seconds of a source written as decimal degrees
// (done once, by hand, and stated in the row's note).
//
// RULES THE ROWS KEEP (owner's and the common law):
//  - a SITE only where a source publishes coordinates; a sensor with a band and no coordinates is NOT here, it is in `leftOut` with why;
//  - frequency limits (lowMHz, highMHz) only where a source publishes limits for THAT sensor; a single published frequency, a centre
//    frequency or a band name goes in the band's name and the notes (the schema has no center-frequency field yet: STATE-LLP.md);
//    a federal allocation (NTIA) is never written as a sensor's band;
//  - retired or planned sensors carry their status (`status`), never an "operating" they do not have;
//  - every row cites its own source (`cite`); the row says when the source is secondary (Wikipedia, CC BY-SA 4.0).
//
// A network is ONE provider on the node (`sdn-ssa-<slug>`), so one network can be withdrawn without touching another.

const WP = (title, slug = title.replace(/ /g, "_")) => ({ url: `https://en.wikipedia.org/wiki/${slug}`, text: `Wikipedia, "${title}" (CC BY-SA 4.0; a secondary source)` });
const TERMS_WP = { class: "ShareAlike", text: "Wikipedia text and tags are CC BY-SA 4.0 (https://creativecommons.org/licenses/by-sa/4.0/); these facts keep that credit and license. Where an operator page is also cited its own terms were not read, and none is claimed." };
const TERMS_UNSTATED = { class: "CiteOnly", text: "The cited sources state no reuse terms that this lane read; the facts (a name, a position, a frequency) are published with their citation on the Space Data Network owner's decision of 2026-11-18, and can be withdrawn." };
const OWNER_DECISION = "Published on the Space Data Network owner's decision of 2026-11-18 (\"publish them all, same as leolabs\"), facts only, with the citation of each row.";

export const NETWORKS = [
  {
    slug: "ussf-ssn", name: "U.S. Space Surveillance Network", operator: "United States Space Force", kind: "government space surveillance network",
    status: "operating (individual sensors carry their own status)", terms: TERMS_WP, credit: "Wikipedia contributors (CC BY-SA 4.0), see each row's URL",
    sites: [
      { slug: "eglin", name: "Eglin AFB Site C-6 (AN/FPS-85 Space Track radar)", abbr: "FPS-85", type: "MILITARY_BASE", kind: "space surveillance radar", country: "840", lat: 30.57333, lon: -86.215, precision: "arc-second (30 34 24 N, 86 12 54 W), facility level",
        where: "Eglin Air Force Base, Walton County, Florida, United States", cite: WP("AN/FPS-85", "AN/FPS-85"), parties: [{ role: "OPERATOR", name: "United States Space Force" }], status: "OPERATING", statusDate: "1969", note: "Operational 1969; Space Force since 2019-12." },
      { slug: "maui", name: "Maui Space Surveillance Complex, Haleakala (GEODSS Maui)", abbr: "GEODSS Maui", type: "OBSERVATION_STATION", kind: "space surveillance optical telescopes", country: "840", lat: 20.70833, lon: -156.25806, alt: 3058, precision: "arc-second, facility level",
        where: "Haleakala, Maui, Hawaii, United States", cite: WP("Maui Space Surveillance Complex"), parties: [{ role: "OPERATOR", name: "United States Space Force (15th Space Surveillance Squadron)" }], status: "OPERATING", note: "GEODSS upgraded 2026-04 (Wikipedia, Ground-based Electro-Optical Deep Space Surveillance)." },
      { slug: "holt", name: "Space Surveillance Telescope, Harold E. Holt, North West Cape", abbr: "SST", type: "OBSERVATION_STATION", kind: "space surveillance optical telescope", country: "036", lat: -21.89567, lon: 114.08989, precision: "8 decimals, facility level",
        where: "Harold E. Holt, North West Cape, Exmouth, Western Australia", cite: WP("Space Surveillance Telescope"), parties: [{ role: "OPERATOR", name: "United States Space Force and the Royal Australian Air Force" }], status: "OPERATING", statusDate: "2022-10-04", note: "Initial operational capability 2022-10-04." },
      { slug: "cobradane", name: "Eareckson Air Station, Shemya (AN/FPS-108 Cobra Dane)", abbr: "Cobra Dane", type: "MILITARY_BASE", kind: "space surveillance radar", country: "840", lat: 52.7373, lon: 174.0914, precision: "4 decimals (about 10 m), facility level",
        where: "Shemya Island, Aleutian Islands, Alaska, United States", cite: WP("Cobra Dane"), parties: [{ role: "OPERATOR", name: "United States Space Force" }], status: "OPERATING", statusDate: "1977", note: "Online 1977." },
      { slug: "parcs", name: "Cavalier Space Force Station (AN/FPQ-16 PARCS)", abbr: "PARCS", type: "MILITARY_BASE", kind: "space surveillance radar", country: "840", lat: 48.73194, lon: -97.90444, precision: "facility level",
        where: "Cavalier Space Force Station, North Dakota, United States", cite: WP("Cavalier Space Force Station"), parties: [{ role: "OPERATOR", name: "United States Space Force" }], status: "OPERATING", note: "Tracks a basketball-size (24 cm) object at 3,000 km (Wikipedia)." },
      { slug: "beale", name: "Beale Air Force Base (PAVE PAWS / UEWR radar)", abbr: "Beale", type: "MILITARY_BASE", kind: "space surveillance radar", country: "840", lat: 39.13611, lon: -121.43639, precision: "the base, not the radar building",
        where: "Beale Air Force Base, California, United States", cite: WP("PAVE PAWS"), parties: [{ role: "OPERATOR", name: "United States Space Force" }], status: "OPERATING", note: "The position is Wikipedia's tag for the base." },
      { slug: "clear", name: "Clear Space Force Station (UEWR radar)", abbr: "Clear", type: "MILITARY_BASE", kind: "space surveillance radar", country: "840", lat: 64.29056, lon: -149.18694, precision: "the base, not the radar building",
        where: "Clear Space Force Station, Alaska, United States", cite: WP("Solid State Phased Array Radar System"), parties: [{ role: "OPERATOR", name: "United States Space Force" }], status: "OPERATING", statusDate: "2001", note: "Last SSPARS site to finish (2001). The position is Wikipedia's tag for the base." },
      { slug: "pituffik", name: "Pituffik Space Base (UEWR radar)", abbr: "Pituffik", type: "MILITARY_BASE", kind: "space surveillance radar", country: "304", lat: 76.53111, lon: -68.70306, precision: "the base, not the radar building",
        where: "Pituffik Space Base, Greenland", cite: WP("Solid State Phased Array Radar System"), parties: [{ role: "OPERATOR", name: "United States Space Force" }], status: "OPERATING", note: "The position is Wikipedia's tag for the base." },
      { slug: "fylingdales", name: "RAF Fylingdales (UEWR radar)", abbr: "Fylingdales", type: "MILITARY_BASE", kind: "space surveillance radar", country: "826", lat: 54.362, lon: -0.67, precision: "3 decimals (the console's own curated row)",
        where: "North Yorkshire, United Kingdom", cite: { url: "https://www.petersonschriever.spaceforce.mil/About-Us/Fact-Sheets/Display/Article/326220/raf-fylingdales-uk", text: "United States Space Force fact sheet, RAF Fylingdales (the place only); the position is the SpaceAware console's own curated row (space-sites.json, S.SSA), which differs by about 8 km from Wikipedia's tag (54.412 N, 0.569 W)" },
        parties: [{ role: "OPERATOR", name: "Royal Air Force and United States Space Force" }], status: "OPERATING", note: "Operated by the Royal Air Force; part of the SSN as a collateral sensor." },
      { slug: "millstone", name: "Millstone Hill Observatory, Westford", abbr: "Millstone", type: "RESEARCH_FACILITY", kind: "space surveillance and incoherent scatter radar", country: "840", lat: 42.61957, lon: -71.49125, precision: "5 decimals",
        where: "Westford, Massachusetts, United States", cite: WP("Millstone Hill"), parties: [{ role: "OPERATOR", name: "MIT Lincoln Laboratory and MIT Haystack Observatory" }], status: "OPERATING", note: "Millstone Hill is a collateral SSN sensor and a research incoherent scatter radar." },
      { slug: "haystack", name: "Haystack Observatory, Westford (HUSIR)", abbr: "HUSIR", type: "RESEARCH_FACILITY", kind: "space imaging radar", country: "840", lat: 42.6233, lon: -71.4882, precision: "facility level",
        where: "Westford, Massachusetts, United States", cite: WP("Haystack Observatory"), parties: [{ role: "OPERATOR", name: "MIT Lincoln Laboratory (for the United States Space Force)" }], status: "OPERATING", note: "Haystack Ultrawideband Satellite Imaging Radar." },
      { slug: "globus", name: "Vardo (Globus II)", abbr: "Globus II", type: "MILITARY_BASE", kind: "space surveillance radar", country: "578", lat: 70.3671, lon: 31.1278, precision: "4 decimals",
        where: "Vardo, Norway", cite: WP("GLOBUS"), parties: [{ role: "OPERATOR", name: "Norwegian Intelligence Service (contributing sensor of the SSN)" }], status: "OPERATING", statusDate: "1995", note: "Built 1995 at Vandenberg; Norway-run since 2001; replacement tested 2018 (Wikipedia)." },
    ],
    sensors: [
      { id: "fps85", site: "eglin", name: "AN/FPS-85 Space Track radar", type: "RADAR_PESA", emission: "ACTIVE", band: { name: "UHF band; centre frequency 442 MHz with 10 MHz bandwidth (Wikipedia); band limits not published", designation: "UHF" }, powerW: 32000000, bandwidthHz: 10000000, cite: WP("AN/FPS-85"),
        note: "Peak power 32 MW (10 kW per transmitter module); phased array facing south; the beam is steered up to 60 degrees from boresight (120 degrees of azimuth), elevation from the horizon to 15 degrees past zenith; tracks a basketball-size object to 41,000 km (Wikipedia)." },
      { id: "geodss-maui", site: "maui", name: "GEODSS Maui telescopes", type: "OPTICAL_TELESCOPE", emission: "PASSIVE", cite: WP("Ground-based Electro-Optical Deep Space Surveillance"), note: "Optical (no radio band); aperture not read." },
      { id: "sst", site: "holt", name: "Space Surveillance Telescope", type: "OPTICAL_TELESCOPE", emission: "PASSIVE", apertureM: 3.5, cite: WP("Space Surveillance Telescope"), note: "3.5 m Mersenne-Schmidt telescope, F/1.0; one Space Surveillance Unit (Royal Australian Air Force) operates it since early 2023." },
      { id: "cobradane", site: "cobradane", name: "AN/FPS-108 Cobra Dane", type: "RADAR_PESA", emission: "ACTIVE", band: { name: "L band 1215 to 1400 MHz (Wikipedia: \"operates in the 1215-1400 MHz band\")", designation: "L", lowMHz: 1215, highMHz: 1400 }, cite: WP("Cobra Dane"),
        note: "One face, 29 m (95 ft) array, 136 degrees of azimuth, facing Kamchatka (Wikipedia). Power not read." },
      { id: "parcs", site: "parcs", name: "AN/FPQ-16 PARCS", type: "PHASED_ARRAY_EARLY_WARNING", emission: "ACTIVE", cite: WP("Cavalier Space Force Station"), note: "Phased array radar; band not published on the page read (the building's power plant, 14 MW, is site power, not radar power)." },
      { id: "beale", site: "beale", name: "PAVE PAWS / UEWR radar, Beale", type: "PHASED_ARRAY_EARLY_WARNING", emission: "ACTIVE",
        band: { name: "UHF band 420 to 450 MHz (Wikipedia gives this for AN/FPS-115 PAVE PAWS; the upgraded AN/FPS-132 limits are not published)", designation: "UHF", lowMHz: 420, highMHz: 450 }, cite: WP("PAVE PAWS"),
        note: "PAVE PAWS: two faces 120 degrees apart, 1,792 elements of 320 W each (580 kW peak per face), gain 38.6 dB, beam 2.2 degrees, 120 degrees of azimuth per face, elevation 3 to 85 degrees, range about 5,600 km (Wikipedia). The federal allocation of 420 to 450 MHz is NOT this record's source." },
      { id: "clear", site: "clear", name: "AN/FPS-132 UEWR, Clear", type: "PHASED_ARRAY_EARLY_WARNING", emission: "ACTIVE", band: { name: "UHF band; limits not published", designation: "UHF" }, cite: WP("Solid State Phased Array Radar System"), note: "Upgraded early warning radar: 120 degrees of azimuth per face, range 4,800 km (Wikipedia)." },
      { id: "pituffik", site: "pituffik", name: "AN/FPS-132 UEWR, Pituffik", type: "PHASED_ARRAY_EARLY_WARNING", emission: "ACTIVE", band: { name: "UHF band; limits not published", designation: "UHF" }, cite: WP("Solid State Phased Array Radar System"), note: "Upgraded early warning radar: 120 degrees of azimuth per face, range 4,800 km (Wikipedia)." },
      { id: "fylingdales", site: "fylingdales", name: "AN/FPS-132 UEWR, Fylingdales", type: "PHASED_ARRAY_EARLY_WARNING", emission: "ACTIVE", band: { name: "UHF band; limits not published", designation: "UHF" }, cite: WP("RAF Fylingdales"), note: "120 degrees of azimuth per face (Wikipedia and the Space Force fact sheet)." },
      { id: "millstone", site: "millstone", name: "Millstone Hill radar", type: "RADAR_PULSE", emission: "ACTIVE", band: { name: "UHF band; transmit frequency 440.2 MHz (MIT slide deck, a single value); band limits not published", designation: "UHF" }, powerW: 2500000, cite: WP("Millstone Hill"),
        note: "Steerable 46 m dish (68 m zenith dish); peak power 2.5 MW; research incoherent scatter radar and collateral SSN sensor. Sources: https://en.wikipedia.org/wiki/Millstone_Hill ; https://www.sgo.fi/Events/RS2012/l/04_Stromme_US_ISRs.pdf (frequency and power)." },
      { id: "husir", site: "haystack", name: "Haystack Ultrawideband Satellite Imaging Radar (HUSIR)", type: "RADAR_PULSE", emission: "ACTIVE", band: { name: "X and W band: Wikipedia says it \"broadcasts and listens in bands at either 10 GHz or 95 GHz\"; limits not published", designation: "X/W" }, cite: WP("Haystack Observatory"), note: "Steerable antenna; the antenna was upgraded in 2006 to 150 GHz capability (Wikipedia)." },
      { id: "globus", site: "globus", name: "Globus II radar", type: "RADAR_PULSE", emission: "ACTIVE", cite: WP("GLOBUS"), note: "Band not published on the page read (a secondary source says X band)." },
    ],
    inorbit: [
      { id: "sbss", name: "Space Based Space Surveillance (SBSS Block 10) visible camera", norad: 37168, host: "SBSS 1 (USA 216)", type: "OPTICAL_TELESCOPE", emission: "PASSIVE", status: "operating status not confirmed",
        cite: { url: "https://www.airandspaceforces.com/weapons/sbss/", text: "Air & Space Forces Magazine, SBSS (initial operational capability 2012-08-17); Boeing, 2010-09-26 press release" },
        verified: "GCAT satcat (planet4589.org, updated 2026 Nov 18 0144:40Z): 37168 SBSS 1 (SBSS Block 10), launched 2010 Sep 26, owner AFSMC, in orbit 631 x 634 km",
        note: "Visible-band optical camera of about 500 lb on a two-axis gimbal (press); the aperture was not found. Sun-synchronous orbit 631 x 634 km." },
      { id: "sensorsat", name: "SensorSat visible CCD camera (ORS-5)", norad: 42921, host: "ORS-5 (SensorSat)", type: "OPTICAL_TELESCOPE", emission: "PASSIVE", status: "operating status not confirmed",
        cite: { url: "https://www.ll.mit.edu/r-d/projects/ors-5-sensorsat", text: "MIT Lincoln Laboratory, ORS-5 SensorSat; eoPortal, ORS-5" },
        verified: "GCAT satcat (planet4589.org, updated 2026 Nov 18 0144:40Z): 42921 ORS-5, launched 2017 Aug 26, owner AFORS, in orbit 599 x 604 km",
        note: "Visible CCD camera with time-delay integration, no gimbal; equatorial orbit 599 x 604 km below the GEO belt; aperture, field and limiting magnitude not found." },
      { id: "msx-sbv", name: "Space Based Visible (SBV) sensor on MSX", norad: 23851, host: "MSX (Midcourse Space Experiment)", type: "OPTICAL_TELESCOPE", emission: "PASSIVE", status: "retired", cite: WP("Space Surveillance Network"),
        verified: "GCAT satcat (planet4589.org, updated 2026 Nov 18 0144:40Z): 23851 MSX, launched 1996 Apr 24, owner BMDO",
        note: "RETIRED: the SBV mission ended 2008-06-02 (Wikipedia, Space Surveillance Network). The spacecraft is still catalogued in orbit." },
    ],
    leftOut: [
      ["Space Fence (AN/FSY-3), Kwajalein", "coordinates not published (a secondary source gives the west side of Kwajalein Island; the test-range tag is the atoll)"],
      ["Space Fence second site, Western Australia", "planned, not confirmed by a source read; no coordinates"],
      ["GEODSS Socorro and Diego Garcia", "site coordinates not published in the sources read (the Wikipedia tags are the town and the island)"],
      ["C-band space surveillance radar, Harold E. Holt", "the radar's own position is not published (only the station centre); C band limits not published"],
      ["DARC Site 1, Exmouth", "under construction; position not published"],
      ["PAVE PAWS Cape Cod", "no coordinate in the research sources (the console has its own curated row)"],
      ["Haystack Auxiliary radar", "not researched: the page returned no data"],
      ["ALTAIR, TRADEX, ALCOR, MMW at Roi-Namur", "per-radar coordinates not published in the sources read (ALTAIR 160 and 422 MHz are the only frequencies found)"],
      ["GSSAP (USA 253, 254, 270, 271, 324, 325; GCAT numbers 40099, 40100, 41744, 41745, 51280, 51281) and Silent Barker (USA 346 to 348; 57836 to 57838)", "the catalog numbers are verified in GCAT, but no sensor or figure was found (the NRO did not disclose the payloads): no sensor is invented"],
      ["STSS Demo 1 and 2 and STSS-ATRR (35937, 35938, 34903)", "missile-tracking infrared satellites, not a space surveillance program in what was read"],
      ["Space Surveillance Telescope at White Sands (Minor Planet Center G45), Lincoln Laboratory ETS (704)", "the Minor Planet Center gives an east longitude and parallax constants, not a latitude: turning them into coordinates is a computation for a module, not for this builder"],
      ["GBR-P, Ascension Island radar, MOSS (Moron, retired), BMEWS (retired), AFSSS VHF fence (closed 2013), AN/FPS-17/79/80 (retired), Baker-Nunn", "no per-sensor coordinates in the sources read"],
    ],
  },
  {
    slug: "eu-sst", name: "EU SST", operator: "EU Space Surveillance and Tracking consortium (France: Ministry of Armed Forces and CNES; radar built by ONERA)", kind: "national sensors in a European consortium",
    status: "operating", terms: TERMS_UNSTATED, credit: "ONERA, https://www.onera.fr/en/site-index/graves-space-surveillance-radar.html",
    sites: [
      { slug: "graves-tx", name: "GRAVES transmitter, Broye-Aubigney-Montseugny", abbr: "GRAVES Tx", type: "OBSERVATION_STATION", kind: "bistatic space surveillance radar, transmit site", country: "250", lat: 47.348, lon: 5.515111, precision: "47 20 52.8 N, 5 30 54.4 E (arc-second), written as decimal degrees here",
        where: "former airfield Broye-Aubigney-Montseugny, Haute-Saone, France", cite: { url: "https://sigidwiki.com/wiki/GRAVES", text: "SigID Wiki, GRAVES (a secondary source); ONERA, https://www.onera.fr/en/site-index/graves-space-surveillance-radar.html (the system)" }, parties: [{ role: "OPERATOR", name: "French Ministry of Armed Forces (built by ONERA)" }], status: "OPERATING", statusDate: "2005-11", note: "Bistatic: this is the transmit site; the receive site is a second record, 364 km south." },
      { slug: "graves-rx", name: "GRAVES receiver, Revest-du-Bion (Plateau d'Albion)", abbr: "GRAVES Rx", type: "OBSERVATION_STATION", kind: "bistatic space surveillance radar, receive site", country: "250", lat: 44.0715, lon: 5.534611, precision: "44 04 17.4 N, 5 32 04.6 E (arc-second), written as decimal degrees here",
        where: "Revest-du-Bion, Plateau d'Albion, France", cite: { url: "https://sigidwiki.com/wiki/GRAVES", text: "SigID Wiki, GRAVES (a secondary source)" }, parties: [{ role: "OPERATOR", name: "French Ministry of Armed Forces (built by ONERA)" }], status: "OPERATING", statusDate: "2005-11", note: "Bistatic: this is the receive site; the transmit site is a second record, 364 km north." },
    ],
    sensors: [
      { id: "graves-rx", site: "graves-rx", name: "GRAVES (receive array)", type: "RADAR_CW", emission: "ACTIVE", band: { name: "VHF band; continuous wave at 143.050 MHz (a single published frequency); band limits not published", designation: "VHF" }, cite: { url: "https://sigidwiki.com/wiki/GRAVES", text: "SigID Wiki, GRAVES; ONERA" },
        note: "A bistatic fence: this record is the receive array; the transmitter is the separate site record 'GRAVES transmitter' (a $SNR names one site). Operational since 2005-11. Fence geometry not published here." },
    ],
    inorbit: [],
    leftOut: [
      ["S3TSR (Spain), Moron AB", "L band 1215 to 1400 MHz is published (ESA papers), but no coordinates: a sensor with a band and no coordinates is not a site"],
      ["GESTRA (Germany), Schmidtenhoehe, Koblenz", "L band 1280 to 1400 MHz is published (Wilden et al., ESA conference paper), but no coordinates"],
      ["BIRALES transmitter (Salto di Quirra) and receiver (Medicina)", "410 to 415 MHz is published (INAF papers), but no coordinates"],
      ["Chilbolton CASTR (UK)", "coordinates and frequency not read"],
      ["MFDR (Italian Air Force), Starbrook (UK), Look Up SORASYS (France)", "not found, or no coordinates published (SORASYS-1 is in Lozere, no coordinate)"],
      ["EU SST sensor roster by country", "published as an image map only; no machine-readable list"],
    ],
  },
  {
    slug: "fhr-tira", name: "Fraunhofer FHR TIRA", operator: "Fraunhofer FHR (for the German Federal Ministry of Defence)", kind: "national space observation radar",
    status: "operating", terms: TERMS_WP, credit: "Fraunhofer FHR, https://www.fhr.fraunhofer.de/en/the-institute/technical-equipment/Space-observation-radar-TIRA.html ; Wikipedia (CC BY-SA 4.0)",
    sites: [
      { slug: "tira", name: "TIRA, Wachtberg", abbr: "TIRA", type: "RESEARCH_FACILITY", kind: "space observation radar", country: "276", lat: 50.616667, lon: 7.129722, precision: "50 37 00 N, 7 07 47 E (arc-second; Wikipedia), written as decimal degrees here; the FHR page gives no coordinate",
        where: "Wachtberg near Bonn, Germany", cite: WP("TIRA"), parties: [{ role: "OPERATOR", name: "Fraunhofer FHR" }], status: "OPERATING", note: "34 m Cassegrain dish in a 47.5 m radome; azimuth 360 degrees, elevation 0 to 90 degrees (FHR page)." },
    ],
    sensors: [
      { id: "tira-l", site: "tira", name: "TIRA L-band tracking radar", type: "RADAR_PULSE", emission: "ACTIVE", band: { name: "L band; 1.33 GHz (a single published frequency); limits not published", designation: "L" }, powerW: 1000000, cite: { url: "https://www.fhr.fraunhofer.de/en/the-institute/technical-equipment/Space-observation-radar-TIRA.html", text: "Fraunhofer FHR page for the frequency; peak power 1 MW is a secondary source (Wikipedia, TIRA)" }, note: "FHR says 'high-power' with no number; 1 MW peak is from a secondary source." },
      { id: "tira-ku", site: "tira", name: "TIRA Ku-band imaging radar", type: "RADAR_PULSE", emission: "ACTIVE", band: { name: "Ku band; 16.7 GHz (a single published frequency); limits not published", designation: "Ku" }, powerW: 13000, cite: { url: "https://www.fhr.fraunhofer.de/en/the-institute/technical-equipment/Space-observation-radar-TIRA.html", text: "Fraunhofer FHR page for the frequency; peak power 13 kW is a secondary source (Wikipedia, TIRA)" }, note: "A 2024 FHR paper says the Ku-band imager will be replaced by a Ka-band radar." },
    ],
    inorbit: [], leftOut: [],
  },
  {
    slug: "esa-ogs", name: "ESA", operator: "European Space Agency (run by the Instituto de Astrofisica de Canarias)", kind: "agency optical ground station",
    status: "operating", terms: { class: "CiteOnly", text: "The ESA website's terms limit its contents to personal and non-commercial use and do not grant redistribution (esa.int/terms-and-conditions). Only facts (a name and a position) from Wikipedia (CC BY-SA 4.0) and the ESA page are published, with their URLs, on the Space Data Network owner's decision of 2026-11-18; ESA can have them withdrawn." }, credit: "ESA, https://www.esa.int ; Wikipedia (CC BY-SA 4.0)",
    sites: [
      { slug: "ogs", name: "ESA Optical Ground Station, Teide Observatory", abbr: "OGS", type: "ASTRONOMICAL_OBSERVATORY", kind: "space debris optical telescope", country: "724", lat: 28.300833, lon: -16.511944, alt: 2400, precision: "28 18 03 N, 16 30 43 W (arc-second), written as decimal degrees here; the mirror gives 28 18 04 N",
        where: "Teide Observatory, Tenerife, Spain", cite: WP("ESA Optical Ground Station", "ESA_Optical_Ground_Station"), parties: [{ role: "OPERATOR", name: "European Space Agency and the Instituto de Astrofisica de Canarias" }], status: "OPERATING", statusDate: "1994", note: "ESA-IAC agreement 1994." },
    ],
    sensors: [
      { id: "ogs", site: "ogs", name: "ESA Space Debris Telescope", type: "OPTICAL_TELESCOPE", emission: "PASSIVE", apertureM: 1, focalM: 13.3, cite: WP("ESA Optical Ground Station", "ESA_Optical_Ground_Station"), note: "1 m aperture, focal length 13.3 m; monthly surveys centred on new moon." },
    ],
    inorbit: [], leftOut: [],
  },
  {
    slug: "eiscat", name: "EISCAT", operator: "EISCAT Scientific Association", kind: "incoherent scatter radars",
    status: "Tromso operating; EISCAT_3D under construction", terms: TERMS_WP, credit: "EISCAT, https://eiscat.se ; Wikipedia (CC BY-SA 4.0)",
    sites: [
      { slug: "tromso", name: "EISCAT Tromso", abbr: "EISCAT Tromso", type: "RESEARCH_FACILITY", kind: "incoherent scatter radar", country: "578", lat: 69.5863, lon: 19.22462, precision: "8 decimals (Wikipedia's EISCAT tag), facility level",
        where: "Tromso, Norway", cite: { url: "https://eiscat.se/eiscat3d-information", text: "EISCAT (frequencies, as summarized); position from the Wikipedia tag" }, parties: [{ role: "OPERATOR", name: "EISCAT Scientific Association" }], status: "OPERATING", note: "Used for space debris campaigns as well as ionospheric research." },
    ],
    sensors: [
      { id: "tromso-uhf", site: "tromso", name: "EISCAT Tromso UHF radar", type: "RADAR_PULSE", emission: "ACTIVE", band: { name: "UHF band; about 930 MHz (a single published value); limits not published", designation: "UHF" }, cite: { url: "https://eiscat.se/eiscat3d-information", text: "EISCAT" }, note: "Steerable dish." },
      { id: "tromso-vhf", site: "tromso", name: "EISCAT Tromso VHF radar", type: "RADAR_PULSE", emission: "ACTIVE", band: { name: "VHF band; 224 MHz (a single published value); limits not published", designation: "VHF" }, cite: { url: "https://eiscat.se/eiscat3d-information", text: "EISCAT" }, note: "Parabolic cylinder antenna." },
    ],
    inorbit: [],
    leftOut: [["EISCAT Svalbard and EISCAT_3D (Skibotn, Karesuvanto, Kaiseniemi)", "no coordinates read; EISCAT_3D is under construction (not operational per EISCAT 2026), first subsystem 637 kW"]],
  },
  {
    slug: "russia", name: "Russian Aerospace Forces", operator: "Russian Aerospace Forces and Space Forces", kind: "early warning radars and optical space surveillance",
    status: "operating (statuses vary by row)", terms: TERMS_WP, credit: "Wikipedia contributors (CC BY-SA 4.0), see each row's URL",
    sites: [
      { slug: "lekhtusi", name: "Voronezh-M radar, Lekhtusi", abbr: "Lekhtusi", type: "MILITARY_BASE", kind: "early warning radar", country: "643", lat: 60.275458, lon: 30.546017, precision: "60 16 31.65 N, 30 32 45.66 E (arc-second), written as decimal degrees here",
        where: "Lekhtusi, near St Petersburg, Russia", cite: WP("Voronezh radar"), parties: [{ role: "OPERATOR", name: "Russian Aerospace Forces" }], status: "OPERATING", statusDate: "2012", note: "Fully operational 2012 (Wikipedia)." },
      { slug: "armavir", name: "Voronezh-DM radar, Armavir", abbr: "Armavir", type: "MILITARY_BASE", kind: "early warning radar", country: "643", lat: 44.925, lon: 40.983889, precision: "about 44 55 30 N, 40 59 02 E (Wikipedia), written as decimal degrees here",
        where: "Armavir, Krasnodar Krai, Russia", cite: WP("Voronezh radar"), parties: [{ role: "OPERATOR", name: "Russian Aerospace Forces" }], status: "OPERATING", note: "Combat duty 2009 or 2013 (sources disagree)." },
      { slug: "barnaul", name: "Voronezh-DM radar, Barnaul", abbr: "Barnaul", type: "MILITARY_BASE", kind: "early warning radar", country: "643", lat: 53.139194, lon: 83.68125, precision: "53 08 21.1 N, 83 40 52.5 E (one source), written as decimal degrees here; a second source gives 53 08 24 N, 83 40 51 E (about 100 m away)",
        where: "Barnaul, Altai Krai, Russia", cite: WP("Voronezh radar"), parties: [{ role: "OPERATOR", name: "Russian Aerospace Forces" }], status: "OPERATING", statusDate: "2017-12", note: "Operational 2017-12." },
      { slug: "orsk", name: "Voronezh-M radar, Orsk", abbr: "Orsk", type: "MILITARY_BASE", kind: "early warning radar", country: "643", lat: 51.273346, lon: 58.95903, precision: "6 decimals",
        where: "Orsk, Orenburg Oblast, Russia", cite: WP("Voronezh radar"), parties: [{ role: "OPERATOR", name: "Russian Aerospace Forces" }], status: "OPERATING", statusDate: "2017-12", note: "Operational 2017-12." },
      { slug: "okno", name: "Okno optical station, Nurek", abbr: "Okno", type: "OBSERVATION_STATION", kind: "space surveillance optical complex", country: "762", lat: 38.281, lon: 69.225, precision: "3 decimals (about 100 m)",
        where: "Nurek, Tajikistan", cite: WP("Okno"), parties: [{ role: "OPERATOR", name: "Russian Space Forces" }], status: "OPERATING", note: "Combat duty 2002 or 2004 (sources disagree); inspected 2025-11 per press." },
    ],
    sensors: [
      { id: "lekhtusi", site: "lekhtusi", name: "Voronezh-M radar (Lekhtusi)", type: "PHASED_ARRAY_EARLY_WARNING", emission: "ACTIVE", band: { name: "VHF band 150 to 200 MHz (Wikipedia, a secondary source; not a Russian primary source)", designation: "VHF", lowMHz: 150, highMHz: 200 }, cite: WP("Voronezh radar"), note: "VHF phased array early warning radar." },
      { id: "armavir", site: "armavir", name: "Voronezh-DM radar (Armavir)", type: "PHASED_ARRAY_EARLY_WARNING", emission: "ACTIVE", band: { name: "UHF band; sources disagree on the wavelength; limits not published", designation: "UHF" }, cite: WP("Voronezh radar"), note: "UHF phased array early warning radar." },
      { id: "barnaul", site: "barnaul", name: "Voronezh-DM radar (Barnaul)", type: "PHASED_ARRAY_EARLY_WARNING", emission: "ACTIVE", band: { name: "UHF band; limits not published", designation: "UHF" }, cite: WP("Voronezh radar"), note: "UHF phased array early warning radar." },
      { id: "orsk", site: "orsk", name: "Voronezh-M radar (Orsk)", type: "PHASED_ARRAY_EARLY_WARNING", emission: "ACTIVE", band: { name: "VHF band 150 to 200 MHz (Wikipedia gives this for Voronezh-M, a secondary source)", designation: "VHF", lowMHz: 150, highMHz: 200 }, cite: WP("Voronezh radar"), note: "VHF phased array early warning radar." },
      { id: "okno", site: "okno", name: "Okno optical complex", type: "OPTICAL_TELESCOPE", emission: "PASSIVE", cite: WP("Okno"), note: "Optical (no radio band); aperture not published." },
    ],
    inorbit: [],
    leftOut: [
      ["Voronezh at Pechora/Vorgashor", "sources disagree on the position and the radar's existence"],
      ["Okno-S, Primorsky Krai", "Wikipedia's tag is flagged uncertain ('little is known')"],
      ["Krona, Krona-N", "coordinates not found"],
      ["ISON (KIAM) observatories", "per-site coordinates not found in the sources read (Minor Planet Center codes would give them)"],
    ],
  },
  {
    slug: "jaxa", name: "JAXA", operator: "Japan Aerospace Exploration Agency (Japan Spaceguard Association)", kind: "agency space debris radar and optical",
    status: "operating", terms: { class: "CiteOnly", text: "JAXA's policy (global.jaxa.jp/policy.html) forbids business or commercial use of its materials without prior permission; a position is a fact read from a map marker on JAXA's page. Published on the Space Data Network owner's decision of 2026-11-18, with the page's URL; JAXA can have it withdrawn." }, credit: "Provided by Japan Aerospace Exploration Agency (JAXA), https://track.sfo.jaxa.jp/en/facilities/kamisaibara_bisei.html",
    sites: [
      { slug: "kamisaibara", name: "Kamisaibara Space Guard Center", abbr: "Kamisaibara", type: "OBSERVATION_STATION", kind: "space debris radar", country: "392", lat: 35.31384, lon: 133.93261, precision: "5 decimals; the marker in the map link on JAXA's page (the page states no coordinate in text)",
        where: "Kamisaibara, Okayama Prefecture, Japan", cite: { url: "https://track.sfo.jaxa.jp/en/facilities/kamisaibara_bisei.html", text: "JAXA, Kamisaibara and Bisei Space Guard Centers (the map marker)" }, parties: [{ role: "OPERATOR", name: "Japan Aerospace Exploration Agency" }], status: "OPERATING", statusDate: "2004-03", note: "Observes LEO debris at about 200 to 1,000 km altitude. In operation since 2004-03 (an IAC paper)." },
      { slug: "bisei", name: "Bisei Space Guard Center", abbr: "Bisei", type: "ASTRONOMICAL_OBSERVATORY", kind: "space debris optical telescopes", country: "392", lat: 34.67243, lon: 133.54457, precision: "5 decimals; the marker in the map link on JAXA's page",
        where: "Bisei, Ibara, Okayama Prefecture, Japan", cite: { url: "https://track.sfo.jaxa.jp/en/facilities/kamisaibara_bisei.html", text: "JAXA, Kamisaibara and Bisei Space Guard Centers (the map marker)" }, parties: [{ role: "OPERATOR", name: "Japan Aerospace Exploration Agency" }], status: "OPERATING", statusDate: "2002-03", note: "Observes GEO debris at about 36,000 km." },
    ],
    sensors: [
      { id: "kamisaibara", site: "kamisaibara", name: "Kamisaibara S-band debris radar", type: "RADAR_PULSE", emission: "ACTIVE", band: { name: "S band (a secondary paper); limits not published", designation: "S" }, cite: { url: "https://iafastro.directory/iac/archive/browse/IAC-08/A6/1/2563/", text: "IAC-08 paper (the band); JAXA page (the facility)" }, note: "Radar for LEO debris." },
      { id: "bisei-1m", site: "bisei", name: "Bisei 1 m telescope", type: "OPTICAL_TELESCOPE", emission: "PASSIVE", apertureM: 1, cite: { url: "https://track.sfo.jaxa.jp/en/facilities/kamisaibara_bisei.html", text: "JAXA" }, note: "1 m telescope." },
      { id: "bisei-50cm", site: "bisei", name: "Bisei 50 cm tracker", type: "OPTICAL_TELESCOPE", emission: "PASSIVE", apertureM: 0.5, cite: { url: "https://track.sfo.jaxa.jp/en/facilities/kamisaibara_bisei.html", text: "JAXA" }, note: "50 cm tracking telescope." },
    ],
    inorbit: [],
    leftOut: [["Japan Air Self-Defense Force SSA radar, Sanyo-Onoda", "position not published (a town); frequency not published; operational 2025-03 per the Ministry of Defense"]],
  },
  {
    slug: "research-radars", name: "Research radars", operator: "universities and research institutes (each row names its own)", kind: "incoherent scatter and atmospheric radars that observe space objects",
    status: "operating or retired (each row says)", terms: TERMS_WP, credit: "Wikipedia contributors (CC BY-SA 4.0), see each row's URL",
    sites: [
      { slug: "jicamarca", name: "Jicamarca Radio Observatory", abbr: "Jicamarca", type: "RESEARCH_FACILITY", kind: "incoherent scatter radar", country: "604", lat: -11.95139, lon: -76.87431, alt: 520, precision: "5 decimals (Wikipedia's tag; the text gives 11 57 05 S, 76 52 27.5 W)",
        where: "near Lima, Peru", cite: WP("Jicamarca Radio Observatory"), parties: [{ role: "OPERATOR", name: "Cornell University (Instituto Geofisico del Peru)" }], status: "OPERATING", note: "300 x 300 m array of 18,432 dipoles." },
      { slug: "mu", name: "MU radar, Shigaraki", abbr: "MU", type: "RESEARCH_FACILITY", kind: "atmospheric radar", country: "392", lat: 34.85, lon: 136.11, precision: "2 decimals (about 1 km)",
        where: "Shigaraki, Shiga, Japan", cite: { url: "https://www.eiscat.rl.ac.uk/radars/otherisr.html", text: "EISCAT list of other incoherent scatter radars" }, parties: [{ role: "OPERATOR", name: "Research Institute for Sustainable Humanosphere, Kyoto University" }], status: "OPERATING", statusDate: "1984", note: "Constructed 1984." },
      { slug: "sondrestrom", name: "Sondrestrom Upper Atmospheric Research Facility, Kangerlussuaq", abbr: "Sondrestrom", type: "RESEARCH_FACILITY", kind: "incoherent scatter radar (retired)", country: "304", lat: 66.9858, lon: -50.945626, precision: "facility level",
        where: "Kangerlussuaq, Greenland", cite: WP("Sondrestrom Upper Atmospheric Research Facility"), parties: [{ role: "OPERATOR", name: "SRI International for the National Science Foundation" }], status: "DECOMMISSIONED", statusDate: "2018", note: "RETIRED: operational from 1983 to 2018." },
      { slug: "arecibo", name: "Arecibo Observatory", abbr: "Arecibo", type: "ASTRONOMICAL_OBSERVATORY", kind: "radio telescope and radar (retired)", country: "630", lat: 18.34417, lon: -66.75278, precision: "8 decimals (Wikipedia's tag)",
        where: "Arecibo, Puerto Rico", cite: WP("Arecibo Observatory"), parties: [{ role: "OPERATOR", name: "National Science Foundation (University of Central Florida)" }], status: "DECOMMISSIONED", statusDate: "2020-11", note: "RETIRED: the cables broke on 2020-08-10 and 2020-11-06 and it was not rebuilt (2022)." },
    ],
    sensors: [
      { id: "jicamarca", site: "jicamarca", name: "Jicamarca incoherent scatter radar", type: "RADAR_PULSE", emission: "ACTIVE", band: { name: "VHF band; about 50 MHz (MIT slide deck; the exact value 49.92 is not confirmed)", designation: "VHF" }, cite: { url: "https://www.sgo.fi/Events/RS2012/l/04_Stromme_US_ISRs.pdf", text: "MIT Haystack slide deck, US incoherent scatter radars" }, note: "Vertical-looking array, steerable by phasing." },
      { id: "mu", site: "mu", name: "MU radar", type: "RADAR_PULSE", emission: "ACTIVE", band: { name: "VHF band; 46.5 MHz (a single published frequency)", designation: "VHF" }, cite: { url: "https://www.eiscat.rl.ac.uk/radars/otherisr.html", text: "EISCAT list of other incoherent scatter radars" }, note: "Atmospheric radar." },
      { id: "sondrestrom", site: "sondrestrom", name: "Sondrestrom incoherent scatter radar (RETIRED)", type: "RADAR_PULSE", emission: "ACTIVE", band: { name: "L band; limits not published", designation: "L" }, status: "retired", cite: WP("Sondrestrom Upper Atmospheric Research Facility"), note: "RETIRED 2018. 32 m steerable dish." },
      { id: "arecibo", site: "arecibo", name: "Arecibo 305 m telescope and radar (RETIRED)", type: "RADAR_PULSE", emission: "ACTIVE", status: "retired", cite: WP("Arecibo Observatory"), note: "RETIRED 2020. The radar frequency is not confirmed (the telescope's range, 47 MHz to 10 GHz, is not a radar band and is not stored)." },
    ],
    inorbit: [],
    leftOut: [["Millstone Hill ISR", "published in the U.S. Space Surveillance Network (one record per sensor)"], ["PFISR", "published in the LeoLabs network (LeoLabs is a user of it)"]],
  },
  {
    slug: "canada-inorbit", name: "Canadian space surveillance satellites", operator: "Canadian Department of National Defence and Canadian Space Agency", kind: "in-orbit space surveillance sensors",
    status: "launched 2013-02-25", terms: TERMS_WP, credit: "eoPortal and SpaceQ pages cited per row; Wikipedia (CC BY-SA 4.0)",
    sites: [], sensors: [],
    inorbit: [
      { id: "sapphire", name: "Sapphire space surveillance telescope", norad: 39088, host: "Sapphire", type: "OPTICAL_TELESCOPE", emission: "PASSIVE", apertureM: 0.15, status: "operating status not confirmed", cite: { url: "https://eoportal.org/satellite-missions/sapphire-space-surveillance", text: "eoPortal, Sapphire (a secondary compendium); https://spaceq.ca/sapphire_canadas_first_military_satellite/" },
        verified: "GCAT satcat (planet4589.org, updated 2026 Nov 18 0144:40Z): 39088 Sapphire, launched 2013 Feb 25, owner DRDC/DND, in orbit",
        note: "15 cm telescope, 1.4 degree field of view; tracks objects at 6,000 to 40,000 km; sun-synchronous orbit about 786 km, inclination 98.55 degrees (eoPortal). Contributes to the U.S. Space Surveillance Network. Operating status not confirmed in the pages read." },
      { id: "neossat", name: "NEOSSat space surveillance and asteroid telescope", norad: 39089, host: "NEOSSat", type: "OPTICAL_TELESCOPE", emission: "PASSIVE", apertureM: 0.15, status: "operating status not confirmed", cite: { url: "https://conference.sdo.esoc.esa.int/proceedings/neosst1/paper/494/NEOSST1-paper494.pdf", text: "ESA NEOSST1 paper 494 (NEOSSat)" },
        verified: "GCAT satcat (planet4589.org, updated 2026 Nov 18 0144:40Z): 39089 NEOSSat, launched 2013 Feb 25, owner CSA, in orbit",
        note: "15 cm Maksutov telescope; two modes, HEOSS (space surveillance) and NESS (near-Earth asteroids); sun-synchronous orbit 750 to 800 km. DRDC and the Canadian Space Agency." },
    ],
    leftOut: [],
  },
  {
    slug: "commercial-inorbit", name: "Commercial in-orbit space surveillance", operator: "Digantara, Vyoma, NorthStar Earth & Space, HEO and True Anomaly", kind: "in-orbit space surveillance sensors",
    status: "operating", terms: TERMS_UNSTATED, credit: "Satnews and Startbase pages cited per row",
    sites: [], sensors: [],
    inorbit: [
      ...[
        { n: 1, norad: 58899, lemur: "Lemur-2 Nimbus2000", status: "operating status not confirmed" },
        { n: 2, norad: 58900, lemur: "Lemur-2 Valhalla", status: "operating status not confirmed" },
        { n: 3, norad: 58902, lemur: "Lemur-2 Oba-Ni-Jesu", status: "retired", decay: " RETIRED: GCAT lists it decayed on 2026 Jun 14." },
        { n: 4, norad: 58903, lemur: "Lemur-2 Cael", status: "operating status not confirmed" },
      ].map((s) => ({
        id: `northstar-${s.n}`, name: `NorthStar ${s.n} (Skylark) space situational awareness sensor`, norad: s.norad, host: `NorthStar ${s.n} (${s.lemur})`, type: "OPTICAL_TELESCOPE", emission: "PASSIVE", status: s.status,
        cite: { url: "https://nanosats.eu/sat/lemur-2-northstar", text: "nanosats.eu, Lemur-2 NorthStar; Satnews, Exolaunch to deploy four Spire-built satellites for NorthStar (2024-01-17)" },
        verified: `GCAT satcat (planet4589.org, updated 2026 Nov 18 0144:40Z): ${s.norad} ${s.lemur}, alt name NorthStar ${s.n}, launched 2024 Jan 31, owner SPIRE/NSTAR`,
        note: `Space situational awareness sensor on a 12-unit cubesat built and operated by Spire; the plan is to track objects down to 5 cm in LEO and 40 cm in GEO (press); no aperture, field or band published. The host is Spire's own Lemur-2 bus, not another customer's satellite.${s.decay ?? ""}`,
      })),
      { id: "heo-adler", name: "HEO Adler non-Earth imaging camera (hosted)", norad: 63223, host: "DROID.002 (Turion Space)", type: "OPTICAL_TELESCOPE", emission: "PASSIVE", status: "operating status not confirmed",
        cite: { url: "https://keeptrack.space/deep-dive/droid-002", text: "KeepTrack, DROID.002 (the hosted HEO cameras); HEO, https://www.heospace.com/cameras" },
        verified: "GCAT satcat (planet4589.org, updated 2026 Nov 18 0144:40Z): 63223 DROID.002 (the host spacecraft), launched 2025 Mar 15, owner TURION",
        note: "A hosted payload: the camera rides on Turion's DROID.002; the catalog number is the host's. Resolved imagery of passing space objects; no aperture found. NOAA's 2023 foreign-benchmark document lists HEO under non-Earth imaging, 400 to 1000 nm (not a licence record, so no band is stored)." },
      { id: "heo-holmes", name: "HEO Holmes Mk1 non-Earth imaging camera (hosted)", norad: 63223, host: "DROID.002 (Turion Space)", type: "OPTICAL_TELESCOPE", emission: "PASSIVE", status: "operating status not confirmed",
        cite: { url: "https://keeptrack.space/deep-dive/droid-002", text: "KeepTrack, DROID.002 (the hosted HEO cameras); HEO, https://www.heospace.com/cameras" },
        verified: "GCAT satcat (planet4589.org, updated 2026 Nov 18 0144:40Z): 63223 DROID.002 (the host spacecraft), launched 2025 Mar 15, owner TURION",
        note: "A hosted payload: the camera rides on Turion's DROID.002; the catalog number is the host's. Resolved imagery of passing space objects; no aperture found." },
      ...[
        { id: "jackal-x-sv1", norad: 59127, gcat: "Jackal X-1L-001", mission: "Jackal Mission X SV1", launched: "2024 Mar 4" },
        { id: "jackal-x-sv2", norad: 59134, gcat: "Jackal X-1L-002", mission: "Jackal Mission X SV2", launched: "2024 Mar 4" },
        { id: "jackal-x2", norad: 62378, gcat: "TANSTAAFL-002", mission: "Jackal Mission X-2", launched: "2024 Dec 21" },
      ].map((s) => ({
        id: s.id, name: `${s.mission} SpectraCam optical camera`, norad: s.norad, host: s.mission, type: "OPTICAL_TELESCOPE", emission: "PASSIVE", status: "operating status not confirmed",
        cite: { url: "https://spacenews.com/redwire-to-supply-cameras-for-true-anomalys-inspector-satellites", text: "SpaceNews, Redwire to supply cameras for True Anomaly's inspector satellites" },
        verified: `GCAT satcat (planet4589.org, updated 2026 Nov 18 0144:40Z): ${s.norad} ${s.gcat} (${s.mission}), launched ${s.launched}, owner TANOM`,
        note: "One Redwire SpectraCam camera (and two SpectraTrac star trackers, not stored) per vehicle; no aperture or band published.",
      })),
      { id: "scot", name: "SCOT space surveillance telescope (Digantara)", norad: 62617, host: "SCOT-Digantara", type: "OPTICAL_TELESCOPE", emission: "PASSIVE", status: "operating", cite: { url: "https://satnews.com/2025/01/14/digantara-launches-indias-1st-homegrown-space-surveillance-mission-via-spacexs-transporter-12/", text: "Satnews, Digantara launches SCOT (2025-01-14, Transporter-12)" },
        verified: "GCAT satcat (planet4589.org, updated 2026 Nov 18 0144:40Z): 62617 SCOT-Digantara, launched 2025 Jan 14, owner DIGAN/ORBAS, in orbit",
        note: "Space-based optical space surveillance satellite (OrbAstro ORB-6 bus), launched on Transporter-12; aperture and field not published. CelesTrak was not reachable: a KeepTrack page URL shows 62615, which conflicts with GCAT's 62617 and was not settled; GCAT's number is used." },
      { id: "flamingo-1", name: "Flamingo optical space surveillance satellite (Vyoma)", norad: 67391, host: "Flamingo-1", type: "OPTICAL_TELESCOPE", emission: "PASSIVE", status: "operating", cite: { url: "https://www.startbase.com/news/vyoma-satellit-flamingo-1-erreicht-sonnensynchrone-umlaufbahn/", text: "Startbase, Vyoma satellite Flamingo-1 reaches its sun-synchronous orbit (launched 2026-01-11)" },
        verified: "GCAT satcat (planet4589.org, updated 2026 Nov 18 0144:40Z): 67391 Flamingo-1, launched 2026 Jan 11, owner VYOMA, in orbit",
        note: "First of a planned 12-satellite in-orbit optical constellation; aperture and field not published." },
    ],
    leftOut: [
      ["Jackal Mission X-3 (69012) and True Anomaly's FCC radio links (call sign WP2XYJ: 2080.2 MHz uplink, 2225 and 8487 MHz downlinks, 15490 MHz)", "no $RFB record: the license names an 'MX3 satellite', not a catalog number, and matching it to a catalog row is an inference; no sensor payload of this vehicle is stated by a cited source"],
      ["Scout Space (SCOUT-Vision, Sparrow, Owl), Turion DROID.001 and DROID.002's own imagers, Astroscale ADRAS-J", "host catalog numbers not looked up, or not a space surveillance sensor"],
      ["Anduril/Numerica Ku-band radar test places (FCC WN2XWH: Briggsdale and three Fort Collins points, 15.7 to 17.2 GHz) and True Anomaly's earth-station places (FCC WP2XYJ)", "licensed test places and earth stations: the licenses do not say space surveillance for the radar and name no sensor at the stations"],
      ["Skylark 5 to 12 (NorthStar)", "not launched per one tracker"],
      ["ExoAnalytic, Slingshot, Kratos, Safran WeTrack, COMSPOC partners, Look Up, ThothX, Sybilla, Airbus GeoTracker, Aldoria, Spaceflux, Share My Space", "ground site locations not published"],
    ],
  },
];

export const OWNER_DECISION_TEXT = OWNER_DECISION;

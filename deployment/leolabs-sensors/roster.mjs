// FACTS ONLY, as LeoLabs' public visualization page shows them (owner 2026-11-18: "We are using their absolutely
// public page with their locations and ranges, just pull the data from their open site").
//
// Source: https://platform.leolabs.space/visualizations/leo  (instrument list, data epoch 2026-11-18T19:01:02Z; the page
// loaded once as a visitor; no file of the site is kept). Credit: LeoLabs, https://leolabs.space .
// Nothing here is computed: every number is a value the page, the AMOS 2021 paper or an FCC license states.
//
// NOT HERE: the field-of-view outlines. They are in leolabs-shapes.mjs, read from the page's data file kept in source/
// (owner 2026-11-20: LeoLabs' permission, "port them over exactly"). Each outline is cut at a 2000 km altitude ceiling for every
// radar, PFISR included (its drawn top point is 2100 km from the radar, which is how the page draws it, not a ceiling).

export const PAGE_URL = "https://platform.leolabs.space/visualizations/leo";
export const PAGE_EPOCH = "2026-11-18T19:01:02.000Z";
export const LEOLABS_URL = "https://leolabs.space";
export const PAPER_URL = "https://amostech.com/TechnicalPapers/2021/Poster/Rowland.pdf";
export const PAPER = "Rowland, McKnight, Prado Pino, Reihs, Stevenson, \"A Worldwide Network of Radars for Space Domain Awareness in Low Earth Orbit\", AMOS 2021";
export const FCC_UHF = { url: "https://apps.fcc.gov/els/GetAtt.html?id=341418", text: "FCC experimental license WO2XME, LeoLabs, Inc., Pearce, Arizona, 430-449 MHz, effective 2024-01-30 (expired 2026-02-01)",
  recordId: "WO2XME", source: "United States Federal Communications Commission, experimental license WO2XME (LeoLabs, Inc.)", license: "United States public record",
  notes: "The license covers the Pearce, Arizona station; it is matched to this radar by band and state." };
// The FCC experimental licenses whose station place matches a radar on the page (SSA-NETWORKS.md third pass, ELS read 2026-11-18).
// A license is a test-station license: it proves the licensed place and band, not the radar's name.
const ELS = "https://apps.fcc.gov/oetcf/els/reports/GenericSearch.cfm";
export const FCC_WI2XJZ = { url: ELS, recordId: "WI2XJZ", source: "United States Federal Communications Commission, experimental license WI2XJZ (LeoLabs, Inc.)", license: "United States public record",
  text: "FCC experimental license WI2XJZ, file 0468-EX-CR-2026, LeoLabs, Inc., Kermit (Winkler), Texas, 31 57 52 N, 103 14 01 W, 430-449 MHz, effective 2026-07-17, expires 2031-08-01 (look up the call sign in the OET Experimental Licensing System)",
  notes: "A fixed UHF test station at Kermit, Texas, within about 20 m of the position the page gives this radar; matched by position, the license does not name the radar." };
export const FCC_WP2XJW = { url: ELS, recordId: "WP2XJW", source: "United States Federal Communications Commission, experimental license WP2XJW (LeoLabs, Inc.)", license: "United States public record",
  text: "FCC experimental license WP2XJW, file 0021-EX-CN-2025, LeoLabs, Inc., Kihei (Maui), Hawaii, 20 44 31 N, 156 26 15 W, 2930-2980 MHz, effective 2025-02-26, expires 2027-03-01 (look up the call sign in the OET Experimental Licensing System)",
  notes: "An S-band test station at Kihei, Maui, within about 50 m of the position the page gives this radar; matched by position, the license does not name the radar. The license says the devices are retrieved after the experiment." };
export const VIDEO ={ url: "https://www.youtube.com/watch?v=Loz-rp4kdpw", text: "LeoLabs, \"Introducing LeoLabs Next-Generation UHF Radar: Seeker\" (video description: UHF planar direct radiating array, unveiled 10 December 2024, Arizona)" };

// Sites. `where` is the place words the sources give; `position` is the page's value for the FIRST radar of the site.
export const SITES = [
  { slug: "pfisr", name: "Poker Flat Incoherent Scatter Radar (PFISR)", abbreviation: "PFISR", siteType: "RESEARCH_FACILITY", country: "840",
    lat: 65.12992, lon: -147.47104, alt: 213, radarIds: ["pfisr"],
    where: "Poker Flat Research Range near Fairbanks, Alaska, United States",
    parties: [
      { role: "OPERATOR", name: "SRI International", cite: "AMISR page: https://amisr.com/amisr/about/about_pfisr/pfisr-specs/" },
      { role: "UNSPECIFIED", name: "National Science Foundation (sponsor of the AMISR radars)", cite: "AMISR page: https://amisr.com/amisr/about/about_pfisr/pfisr-specs/" },
      { role: "UNSPECIFIED", name: "LeoLabs, Inc. (a user of the radar, by partnership)", cite: "LeoLabs: https://www.leolabs.space/radars/" },
    ],
    note: "This radar is operated by SRI International for the National Science Foundation; LeoLabs is a user of it, not its operator." },
  { slug: "msr", name: "Midland Space Radar", abbreviation: "MSR", siteType: "OBSERVATION_STATION", country: "840",
    lat: 31.964245, lon: -103.233543, alt: 883, radarIds: ["msr"],
    where: "Texas, United States",
    parties: [{ role: "OPERATOR", name: "LeoLabs, Inc.", cite: "LeoLabs: https://www.leolabs.space/radars/" }],
    note: "The page places the radar in Winkler County near Kermit, Texas (the FCC test point of license WO2XXL is within about 50 m of it). Press reports (Midland Reporter-Telegram) place it on T-Bar Ranch southwest of Midland. This record uses the page's position." },
  { slug: "ksr", name: "Kiwi Space Radar", abbreviation: "KSR", siteType: "OBSERVATION_STATION", country: "554",
    lat: -45.03854, lon: 170.09556, alt: 609.07, radarIds: ["ksr1", "ksr2"],
    where: "Central Otago, South Island, New Zealand",
    parties: [{ role: "OPERATOR", name: "LeoLabs, Inc.", cite: "LeoLabs: https://www.leolabs.space/radars/" }],
    note: "Two radars (north and south). The position is the north radar's; the south radar stands at -45.0389, 170.09585, 608.46 m on the page." },
  { slug: "crsr", name: "Costa Rica Space Radar", abbreviation: "CRSR", siteType: "OBSERVATION_STATION", country: "188",
    lat: 10.4133, lon: -85.480465, alt: 24, radarIds: ["crsr1", "crsr2"],
    where: "Guanacaste, Costa Rica",
    parties: [{ role: "OPERATOR", name: "LeoLabs, Inc.", cite: "LeoLabs: https://www.leolabs.space/radars/" }],
    note: "Two radars (west and east), commissioned 22 April 2021 in Guanacaste (AMOS 2021 paper). The position is the west radar's; the east radar stands at 10.413456667, -85.479985, 23 m on the page." },
  { slug: "azsr", name: "Azores Space Radar", abbreviation: "AZSR", siteType: "OBSERVATION_STATION", country: "620",
    lat: 36.9975, lon: -25.1384933, alt: 198, radarIds: ["azsr1", "azsr2"],
    where: "Santa Maria Island, Azores, Portugal",
    parties: [{ role: "OPERATOR", name: "LeoLabs, Inc.", cite: "LeoLabs: https://www.leolabs.space/radars/" }],
    note: "Two radars (north and south). The position is the north radar's; the south radar stands at 36.9971033, -25.1386383, 198 m on the page." },
  { slug: "wasr", name: "West Australian Space Radar", abbreviation: "WASR", siteType: "OBSERVATION_STATION", country: "036",
    lat: -33.308487, lon: 116.030608, alt: 241, radarIds: ["wasr1", "wasr2"],
    where: "Western Australia, Australia",
    parties: [{ role: "OPERATOR", name: "LeoLabs, Inc.", cite: "LeoLabs: https://www.leolabs.space/radars/" }],
    note: "Two radars (north and south). The position is the north radar's; the south radar stands at -33.308925, 116.030545, 242 m on the page." },
  { slug: "rasr", name: "Arizona Space Radar (Seeker)", abbreviation: "Seeker", siteType: "OBSERVATION_STATION", country: "840",
    lat: 31.8711231, lon: -109.50613596, alt: 1441.774, radarIds: ["rasr1"],
    where: "Pearce, Cochise County, Arizona, United States (the place FCC license WO2XME names; matched to the page's radar by band and state)",
    parties: [{ role: "OPERATOR", name: "LeoLabs, Inc.", cite: "LeoLabs: https://www.leolabs.space/radars/ ; FCC license WO2XME" }],
    note: "LeoLabs' UHF planar radar (Seeker), a test installation." },
  { slug: "hisr", name: "SCOUT Hawaii Space Radar", abbreviation: "Scout Hawaii", siteType: "OBSERVATION_STATION", country: "840",
    lat: 20.742, lon: -156.437, alt: 86.715, radarIds: ["hisr1"],
    where: "Hawaii, United States",
    parties: [{ role: "OPERATOR", name: "LeoLabs, Inc.", cite: "LeoLabs: https://www.leolabs.space/radars/" }],
    note: "Scout is transportable; this site is the position the page shows now." },
];

// Radars. powerW and freqMHz are the page's `transmitPower` and `transmitFrequency` (one value each, not a band).
// bandLowMHz / bandHighMHz only where a source PUBLISHES limits (Seeker: FCC WO2XME). az / el: the page's boresight.
export const RADARS = [
  { id: "pfisr", site: "pfisr", name: "Poker Flat Incoherent Scatter Radar", band: "UHF", freqMHz: 450, powerW: 2000000, az: 15, el: 74, kind: "fan" },
  { id: "msr", site: "msr", name: "Midland Space Radar", band: "UHF", freqMHz: 435, powerW: 65000, az: -18, el: 90, kind: "fan",
    bandLowMHz: 430, bandHighMHz: 449, bandCite: FCC_WI2XJZ },
  { id: "ksr1", site: "ksr", name: "Kiwi Space Radar - North", band: "S", freqMHz: 2950, powerW: 51200, az: -13, el: 70, kind: "fan" },
  { id: "ksr2", site: "ksr", name: "Kiwi Space Radar - South", band: "S", freqMHz: 2960, powerW: 51200, az: 167, el: 70, kind: "fan" },
  { id: "crsr1", site: "crsr", name: "Costa Rica Space Radar - West", band: "S", freqMHz: 2940, powerW: 52000, az: -63, el: 75, kind: "fan" },
  { id: "crsr2", site: "crsr", name: "Costa Rica Space Radar - East", band: "S", freqMHz: 2960, powerW: 52000, az: 27, el: 75, kind: "fan" },
  { id: "azsr1", site: "azsr", name: "Azores Space Radar - North", band: "S", freqMHz: 2950, powerW: 52000, az: 16, el: 75, kind: "fan" },
  { id: "azsr2", site: "azsr", name: "Azores Space Radar - South", band: "S", freqMHz: 2960, powerW: 52000, az: -164, el: 75, kind: "fan" },
  { id: "wasr1", site: "wasr", name: "West Australian Space Radar - North", band: "S", freqMHz: 2938, powerW: 52000, az: 35, el: 75, kind: "fan" },
  { id: "wasr2", site: "wasr", name: "West Australian Space Radar - South", band: "S", freqMHz: 2962, powerW: 52000, az: -145, el: 75, kind: "fan" },
  { id: "rasr1", site: "rasr", name: "Arizona Space Radar (Seeker)", band: "UHF", freqMHz: 435, powerW: 128000, az: 0, el: 90, kind: "planar",
    bandLowMHz: 430, bandHighMHz: 449, bandCite: FCC_UHF },
  { id: "hisr1", site: "hisr", name: "SCOUT Hawaii Space Radar", band: "S", freqMHz: 2950, powerW: 102000, az: 0, el: 90, kind: "planar",
    bandLowMHz: 2930, bandHighMHz: 2980, bandCite: FCC_WP2XJW },
];

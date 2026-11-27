// ROUND 2 (2026-11-19): the sensors and networks added after the first publication. Same rules as networks.mjs, with one change the owner
// asked for by naming the networks ("add space fence"; "Also need Chinese Russian Iranian and all other sensor networks"): where no source
// publishes the position of the sensor itself, a row may carry the published position of the place that holds it (the base, the launch
// center, the observatory, the island, the town), and its `precision` says in words that this is what the number is.
// Every row was read from its cited page by one research pass and re-read from that page by a second, independent pass.
// Rows were collected into ssa-round2/checked-*.json and written here once; this file is the source from then on.

const TERMS_WP = { class: "ShareAlike", text: "Wikipedia text and tags are CC BY-SA 4.0 (https://creativecommons.org/licenses/by-sa/4.0/); these facts keep that credit and license. Where another page is also cited its own terms were not read, and none is claimed." };
const TERMS_UNSTATED = { class: "CiteOnly", text: "The cited sources state no reuse terms that this lane read; the facts (a name, a position, a frequency) are published with their citation on the Space Data Network owner's decision of 2026-11-19, and can be withdrawn." };

// Rows added to networks that round 1 already published (keyed by the network's slug).
export const ADDITIONS = {
 "ussf-ssn": {
  "sites": [
   {
    "slug": "kwajalein-space-fence",
    "name": "Space Fence, Kwajalein Island",
    "abbr": "SF-KWAJ",
    "type": "MILITARY_BASE",
    "kind": "radar site",
    "country": "584",
    "lat": 8.7167,
    "lon": 167.7333,
    "precision": "The Wikipedia coordinate tag for Kwajalein Atoll (8 deg 43 min N, 167 deg 44 min E, written as decimal degrees); it is the atoll, not the radar. The radar is on Kwajalein Island (west side) inside the atoll.",
    "where": "Kwajalein Island, Kwajalein Atoll, Marshall Islands (west side of the island)",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Kwajalein_Atoll",
     "text": "Wikipedia Kwajalein Atoll: tag 8 43 N 167 44 E; text says Kwajalein Island hosts the Space Fence radar. Island location also in https://www.airandspaceforces.com/weapons/an-fsy-3/"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "US Space Force, 20th Space Surveillance Squadron"
     }
    ],
    "status": "OPERATING",
    "statusDate": "2020-03-28",
    "note": "Operational declaration 2020-03-28 per Wikipedia Space Fence. A second Space Fence site was only planned/optional; not built per pages read."
   },
   {
    "slug": "roi-namur-rts",
    "name": "Roi-Namur, Reagan Test Site radars",
    "abbr": "RTS-RN",
    "type": "RESEARCH_FACILITY",
    "kind": "space tracking site",
    "country": "584",
    "lat": 9.39611,
    "lon": 167.47583,
    "precision": "Wikipedia coordinate tag of Roi-Namur island (9 23 46 N, 167 28 33 E written as decimal degrees); it is the island, not an individual radar. Radars ALTAIR, ALCOR, MMW and TRADEX are on its Namur section per Wikipedia.",
    "where": "Roi-Namur, northern Kwajalein Atoll, Marshall Islands",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Roi-Namur",
     "text": "Wikipedia Roi-Namur: tag and radar list (ALCOR, ALTAIR, MMW, TRADEX)"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "US Army Reagan Test Site (Kiernan Reentry Measurements Site, MIT Lincoln Laboratory resident team)"
     }
    ],
    "status": "OPERATING",
    "note": ""
   },
   {
    "slug": "diego-garcia-geodss",
    "name": "GEODSS Diego Garcia",
    "abbr": "GEODSS-DG",
    "type": "OBSERVATION_STATION",
    "kind": "space tracking site",
    "country": "086",
    "lat": -7.31333,
    "lon": 72.41111,
    "precision": "Wikipedia coordinate tag of Diego Garcia island (7 18 48 S, 72 24 40 E written as decimal degrees); the island, not the telescopes. The GEODSS site is on the island.",
    "where": "Diego Garcia, British Indian Ocean Territory",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Diego_Garcia",
     "text": "Wikipedia Diego_Garcia tag. Site existence: https://www.airandspaceforces.com/weapons/geodss/"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "US Space Force, 15th Space Surveillance Squadron Det. 2"
     }
    ],
    "status": "OPERATING",
    "note": ""
   },
   {
    "slug": "cape-cod-sfs",
    "name": "Cape Cod Space Force Station",
    "abbr": "CCSFS-PP",
    "type": "MILITARY_BASE",
    "kind": "space tracking site",
    "country": "840",
    "lat": 41.7532,
    "lon": -70.5385,
    "precision": "Wikipedia coordinate tag of the station (41 45 12 N, 70 32 19 W); the radar is on the station, tag is the station, not the array.",
    "where": "Cape Cod Space Force Station, Massachusetts",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Cape_Cod_Space_Force_Station",
     "text": "Wikipedia Cape Cod Space Force Station: tag, listed operational, secondary mission tracking LEO objects reported to 18th Space Control Squadron"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "US Space Force, 6th Space Warning Squadron"
     }
    ],
    "status": "OPERATING",
    "note": ""
   },
   {
    "slug": "harold-e-holt",
    "name": "Naval Communication Station Harold E. Holt",
    "abbr": "HOLT",
    "type": "MILITARY_BASE",
    "kind": "space tracking site",
    "country": "036",
    "lat": -21.816405,
    "lon": 114.16563,
    "precision": "Wikipedia station tag (21 48 59 S, 114 09 56 E; page decimals -21.816405, 114.16563); the station, not the C-band radar.",
    "where": "North West Cape, near Exmouth, Western Australia",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Harold_E._Holt_Naval_Communication_Station",
     "text": "Wikipedia Harold E. Holt station: C-band space surveillance radar and SST at the station"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Australian Defence Force / US Space Force (1 Space Surveillance Unit remote operation)"
     }
    ],
    "status": "OPERATING",
    "note": ""
   },
   {
    "slug": "darc-site-1-exmouth",
    "name": "DARC Site 1, Exmouth",
    "abbr": "DARC1",
    "type": "MILITARY_BASE",
    "kind": "space tracking site",
    "country": "036",
    "lat": -21.905231,
    "lon": 114.114462,
    "precision": "Single point published by the Nautilus Institute as the page's Location; the page does not say whether it marks the transmit array, the receive array or the whole site (two facilities several km apart).",
    "where": "Near Exmouth, Western Australia",
    "cite": {
     "url": "https://nautilus.org/briefing-books/deep-space-advanced-radar-capability-site-1-exmouth/",
     "text": "Nautilus DARC Site 1 page: location, 27 dishes planned, construction Oct 2023, early use approved Sept 2025, full operation expected 2027"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "US Space Force with Australia and UK (AUKUS DARC)"
     }
    ],
    "status": "UNDER_CONSTRUCTION",
    "note": ""
   },
   {
    "slug": "moron-air-base",
    "name": "Moron Air Base (MOSS)",
    "abbr": "LEMO",
    "type": "MILITARY_BASE",
    "kind": "space tracking site",
    "country": "724",
    "lat": 37.17472,
    "lon": -5.61583,
    "precision": "Wikipedia tag of Moron Air Base (37 10 29 N, 5 36 57 W written as decimal degrees); the base, not the telescope.",
    "where": "Moron Air Base, near Seville, Spain",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Mor%C3%B3n_Air_Base",
     "text": "Wikipedia Moron Air Base tag. MOSS facts from Wikipedia GEODSS article"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Spanish Air Force base; MOSS was US Air Force"
     }
    ],
    "status": "OPERATING",
    "note": "Base operating; MOSS sensor is closed."
   },
   {
    "slug": "socorro-geodss",
    "name": "GEODSS Socorro (White Sands, Det. 1)",
    "abbr": "GEODSS Socorro",
    "type": "OBSERVATION_STATION",
    "kind": "space surveillance optical telescopes",
    "country": "840",
    "lat": 33.8172,
    "lon": -106.6599,
    "alt": 1240,
    "precision": "4 decimals, Stallion Range Center tag at northern White Sands Missile Range near Socorro, NM",
    "where": "White Sands Missile Range (Stallion Range Center), near Socorro, New Mexico, United States",
    "cite": {
     "url": "https://www.airandspaceforces.com/weapons/geodss/",
     "text": "Air and Space Forces GEODSS: Det. 1 Socorro NM at White Sands Missile Range, three telescopes, operational"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "United States Space Force (Det. 1, 15th Space Surveillance Squadron)"
     }
    ],
    "status": "OPERATING",
    "note": "Operated by Detachment 1, 15th Space Surveillance Squadron."
   }
  ],
  "sensors": [
   {
    "id": "space-fence-an-fsy-3",
    "site": "kwajalein-space-fence",
    "name": "Space Fence AN/FSY-3",
    "type": "RADAR_AESA",
    "emission": "ACTIVE",
    "status": "OPERATING",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Space_Fence",
     "text": "Wikipedia Space Fence: S-band, operational 2020-03-28; https://www.airandspaceforces.com/weapons/an-fsy-3/ confirms S-band, Kwajalein Atoll, 360 deg azimuth, +20 to +90 deg elevation."
    },
    "note": "Sensor type: phased-array surveillance radar; schema has no better value. Power not published on pages read. 360 deg azimuth, 20-90 deg elevation per Air and Space Forces."
   },
   {
    "id": "altair",
    "site": "roi-namur-rts",
    "name": "ALTAIR",
    "type": "RADAR_PULSE",
    "emission": "ACTIVE",
    "powerW": 5000000,
    "apertureM": 45.7,
    "status": "OPERATING",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/ARPA_Long-Range_Tracking_And_Instrumentation_Radar",
     "text": "Wikipedia ALTAIR: 162 and 422 MHz, 45.7 m dish, 5 MW, operational 1969, still in use; Lincoln Lab page https://www.ll.mit.edu/about/facilities/reagan-test-site lists SSN role, 104 h/week"
    },
    "note": ""
   },
   {
    "id": "tradex",
    "site": "roi-namur-rts",
    "name": "TRADEX",
    "type": "RADAR_PULSE",
    "emission": "ACTIVE",
    "status": "OPERATING",
    "cite": {
     "url": "https://www.ll.mit.edu/about/facilities/reagan-test-site",
     "text": "Lincoln Lab RTS page: operational 1962, SSN contributing sensor since 1998, 104 h/week"
    },
    "note": ""
   },
   {
    "id": "alcor",
    "site": "roi-namur-rts",
    "name": "ALCOR",
    "type": "RADAR_PULSE",
    "emission": "ACTIVE",
    "status": "OPERATING",
    "cite": {
     "url": "https://www.ll.mit.edu/about/facilities/reagan-test-site",
     "text": "Lincoln Lab RTS page: about 1970, 20 h/week space surveillance"
    },
    "note": ""
   },
   {
    "id": "mmw",
    "site": "roi-namur-rts",
    "name": "Millimeter Wave radar (MMW)",
    "type": "RADAR_PULSE",
    "emission": "ACTIVE",
    "status": "OPERATING",
    "cite": {
     "url": "https://www.ll.mit.edu/about/facilities/reagan-test-site",
     "text": "Lincoln Lab RTS page: 1983, 60 h/week space surveillance"
    },
    "note": ""
   },
   {
    "id": "geodss-diego-garcia",
    "site": "diego-garcia-geodss",
    "name": "GEODSS Diego Garcia (Det. 2)",
    "type": "OPTICAL_TELESCOPE",
    "emission": "PASSIVE",
    "status": "OPERATING",
    "cite": {
     "url": "https://www.airandspaceforces.com/weapons/geodss/",
     "text": "Air and Space Forces GEODSS: three telescopes per site, Det. 2 Diego Garcia, active"
    },
    "note": "Aperture not stated on pages opened; not entered. Site listed as operated by Det. 2, 15th SPSS on the Air and Space Forces page."
   },
   {
    "id": "pave-paws-cape-cod",
    "site": "cape-cod-sfs",
    "name": "PAVE PAWS Cape Cod",
    "type": "PHASED_ARRAY_EARLY_WARNING",
    "emission": "ACTIVE",
    "band": {
     "name": "UHF band; the AN/FPS-115 design value is 420-450 MHz per the PAVE PAWS article, but the station page is inconsistent on the current radar designation, so limits are not entered",
     "designation": ""
    },
    "status": "OPERATING",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Cape_Cod_Space_Force_Station",
     "text": "Station page: operational; secondary mission tracking LEO objects reported to 18th SPCS. Band design value: https://en.wikipedia.org/wiki/PAVE_PAWS"
    },
    "note": "Station page lists AN/FPS-123 in infobox and AN/FPS-132 in text; limits not entered. PAVE PAWS article gives 420-450 MHz for AN/FPS-115 only."
   },
   {
    "id": "hax",
    "site": "haystack",
    "name": "Haystack Auxiliary Radar (HAX)",
    "type": "RADAR_PULSE",
    "emission": "ACTIVE",
    "apertureM": 12,
    "status": "OPERATING",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Haystack_Observatory",
     "text": "Wikipedia: HAX 12 m dish, Ku-band, built 1993, contributes data to SSN"
    },
    "note": ""
   },
   {
    "id": "holt-c-band",
    "site": "harold-e-holt",
    "name": "C-Band Space Surveillance Radar, Holt",
    "type": "RADAR_PULSE",
    "emission": "ACTIVE",
    "status": "OPERATING",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Harold_E._Holt_Naval_Communication_Station",
     "text": "Wikipedia Holt station: a C-band space surveillance radar was installed at the station in 2022; No. 1 Space Surveillance Unit operates it remotely from early 2023."
    },
    "note": "Install date conflict: Wikipedia says 2022, DVIDS article of 2018 implies earlier full operational capability; no relocation-from-Antigua claim is on the DVIDS page, so it was removed. Band letter only; limits not published."
   },
   {
    "id": "darc-site-1",
    "site": "darc-site-1-exmouth",
    "name": "DARC Site 1 radar",
    "type": "RADAR_PULSE",
    "emission": "ACTIVE",
    "status": "UNDER_CONSTRUCTION",
    "cite": {
     "url": "https://nautilus.org/briefing-books/deep-space-advanced-radar-capability-site-1-exmouth/",
     "text": "Nautilus: 27 dishes planned, 21 built by Oct 2025; early use approved Sept 2025; full operation expected 2027; band not given."
    },
    "note": "Dish array, not a phased array; schema value is approximate."
   },
   {
    "id": "moss",
    "site": "moron-air-base",
    "name": "MOSS (Moron Optical Space Surveillance)",
    "type": "OPTICAL_TELESCOPE",
    "emission": "PASSIVE",
    "apertureM": 0.56,
    "status": "retired",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Ground-Based_Electro-Optical_Deep_Space_Surveillance",
     "text": "Wikipedia GEODSS: MOSS transportable 22-inch telescope at Moron 1997-2012, closed"
    },
    "note": "RETIRED."
   },
   {
    "id": "geodss-socorro",
    "site": "socorro-geodss",
    "name": "GEODSS Socorro (Det. 1)",
    "type": "OPTICAL_TELESCOPE",
    "emission": "PASSIVE",
    "status": "OPERATING",
    "cite": {
     "url": "https://www.airandspaceforces.com/weapons/geodss/",
     "text": "Air and Space Forces GEODSS: three telescopes per site, Det. 1 Socorro NM, active"
    },
    "note": "Deep-space electro-optical surveillance telescope system at White Sands Missile Range."
   }
  ],
  "inorbit": [
   {
    "id": "gssap-1",
    "name": "GSSAP 1 (USA 253)",
    "norad": 40099,
    "host": "GSSAP",
    "type": "OPTICAL_TELESCOPE",
    "emission": "PASSIVE",
    "status": "Non-operational per catalog page",
    "cite": {
     "url": "https://space.skyrocket.de/doc_sdat/gssap-1.htm",
     "text": "Gunter's Space Page: GSSAP satellites carry electro-optical sensors to collect information on objects in near-geosynchronous region"
    },
    "verified": "public catalog page: https://keeptrack.space/satellite/40099 (NORAD 40099, 2014-043A)",
    "note": ""
   },
   {
    "id": "gssap-2",
    "name": "GSSAP 2 (USA 254)",
    "norad": 40100,
    "host": "GSSAP",
    "type": "OPTICAL_TELESCOPE",
    "emission": "PASSIVE",
    "status": "Non-operational per keeptrack catalog page",
    "cite": {
     "url": "https://space.skyrocket.de/doc_sdat/gssap-1.htm",
     "text": "Gunter's Space Page: electro-optical sensors"
    },
    "verified": "public catalog page: https://keeptrack.space/satellite/40100 (NORAD 40100, 2014-043B)",
    "note": ""
   },
   {
    "id": "gssap-3",
    "name": "GSSAP 3 (USA 270)",
    "norad": 41744,
    "host": "GSSAP",
    "type": "OPTICAL_TELESCOPE",
    "emission": "PASSIVE",
    "status": "Operational per catalog page",
    "cite": {
     "url": "https://space.skyrocket.de/doc_sdat/gssap-1.htm",
     "text": "Gunter's Space Page: electro-optical sensors"
    },
    "verified": "public catalog page: https://keeptrack.space/satellite/41744 (NORAD 41744, 2016-052A)",
    "note": ""
   },
   {
    "id": "gssap-4",
    "name": "GSSAP 4 (USA 271)",
    "norad": 41745,
    "host": "GSSAP",
    "type": "OPTICAL_TELESCOPE",
    "emission": "PASSIVE",
    "status": "Operational per catalog page",
    "cite": {
     "url": "https://space.skyrocket.de/doc_sdat/gssap-1.htm",
     "text": "Gunter's Space Page: electro-optical sensors"
    },
    "verified": "public catalog page: https://keeptrack.space/satellite/41745 (NORAD 41745, 2016-052B)",
    "note": ""
   },
   {
    "id": "gssap-5",
    "name": "GSSAP 5 (USA 324)",
    "norad": 51280,
    "host": "GSSAP",
    "type": "OPTICAL_TELESCOPE",
    "emission": "PASSIVE",
    "status": "Operational per catalog page",
    "cite": {
     "url": "https://space.skyrocket.de/doc_sdat/gssap-1.htm",
     "text": "Gunter's Space Page: electro-optical sensors"
    },
    "verified": "public catalog page: https://keeptrack.space/satellite/51280 (NORAD 51280, 2022-006A)",
    "note": ""
   },
   {
    "id": "gssap-6",
    "name": "GSSAP 6 (USA 325)",
    "norad": 51281,
    "host": "GSSAP",
    "type": "OPTICAL_TELESCOPE",
    "emission": "PASSIVE",
    "status": "Operational per catalog page",
    "cite": {
     "url": "https://space.skyrocket.de/doc_sdat/gssap-1.htm",
     "text": "Gunter's Space Page: electro-optical sensors"
    },
    "verified": "public catalog page: https://keeptrack.space/satellite/51281 (NORAD 51281, 2022-006B)",
    "note": ""
   },
   {
    "id": "sapphire",
    "name": "Sapphire space surveillance telescope (SSN contributing)",
    "norad": 39088,
    "host": "Sapphire",
    "type": "OPTICAL_TELESCOPE",
    "emission": "PASSIVE",
    "apertureM": 0.15,
    "status": "OPERATING",
    "cite": {
     "url": "https://eoportal.org/satellite-missions/sapphire-space-surveillance",
     "text": "eoPortal, Sapphire; Canadian Department of National Defence contributing sensor to the U.S. Space Surveillance Network"
    },
    "verified": "GCAT satcat (planet4589.org, updated 2026 Nov 18 0144:40Z): 39088 Sapphire, launched 2013 Feb 25, owner DRDC/DND, in orbit",
    "note": "15 cm electro-optical space surveillance telescope contributing observations of 6,000 to 40,000 km deep-space objects to the U.S. Space Surveillance Network."
   },
   {
    "id": "neossat",
    "name": "NEOSSat space surveillance telescope (HEOSS)",
    "norad": 39089,
    "host": "NEOSSat",
    "type": "OPTICAL_TELESCOPE",
    "emission": "PASSIVE",
    "apertureM": 0.15,
    "status": "OPERATING",
    "cite": {
     "url": "https://conference.sdo.esoc.esa.int/proceedings/neosst1/paper/494/NEOSST1-paper494.pdf",
     "text": "ESA NEOSST1 paper 494 (NEOSSat High Earth Orbit Space Surveillance)"
    },
    "verified": "GCAT satcat (planet4589.org, updated 2026 Nov 18 0144:40Z): 39089 NEOSSat, launched 2013 Feb 25, owner CSA/DRDC, in orbit",
    "note": "15 cm Maksutov telescope in sun-synchronous orbit (785 km) tracking deep-space HEO/GEO objects in HEOSS mode."
   }
  ],
  "leftOut": [
   [
    "LRDR at Clear",
    "Wikipedia gives no coordinate for the radar and says nothing confirming a Space Surveillance Network role; it is a missile defense radar. Station tag exists (64.29056, -149.18694) but SSN contribution unverified."
   ],
   [
    "Ascension Island radar (FPQ-15)",
    "Only informal sources; current SSN status and coordinates not confirmed."
   ],
   [
    "MCAT (NASA 1.3 m, Ascension)",
    "NASA orbital debris sensor, not USSF network; no coordinates stated (installed June 2015 per Sky and Telescope)."
   ],
   [
    "Cobra King",
    "Shipborne radar on USNS Howard O. Lorenzen; no fixed site; Wikipedia gives no band or SSN role."
   ],
   [
    "GEODSS Maui, Pituffik, others",
    "Already published or outside this assignment."
   ],
   [
    "Silent Barker USA 346, 347, 348 (NORAD 57836, 57837, 57838)",
    "Catalog numbers seen on https://keeptrack.space/satellites/buses/silentbarker; purpose is space surveillance per designation-systems.net, but no opened page names the sensor type, so no in-orbit row"
   ],
   [
    "GSSAP 7 and 8 (2026-029A/B, launched 12 Feb 2026)",
    "Skyrocket page lists them (USA 582/583, 2026-029A/B) but no NORAD number; none confirmed on a catalog page"
   ],
   [
    "Silent Barker",
    "Wikipedia page not found; no source opened"
   ]
  ],
  "nowPublished": [
   "Space Fence (AN/FSY-3), Kwajalein",
   "C-band space surveillance radar, Harold E. Holt",
   "DARC Site 1, Exmouth",
   "PAVE PAWS Cape Cod",
   "Haystack Auxiliary radar",
   "ALTAIR, TRADEX, ALCOR, MMW at Roi-Namur"
  ]
 },
 "russia": {
  "sites": [
   {
    "slug": "voronezh-pionersky",
    "name": "Voronezh-DM radar, Pionersky (Kaliningrad)",
    "abbr": "Voronezh-DM radar,.",
    "type": "MILITARY_BASE",
    "kind": "space tracking site",
    "country": "643",
    "lat": 54.85722,
    "lon": 20.18222,
    "precision": "Per-site coordinate printed in the Voronezh radar article table (54 51 26 N, 20 10 56 E; decimal in table 54.857294, 20.18235), converted from degrees-minutes-seconds; the radar family article, not a tag on its own article",
    "where": "Pionersky, former Dunayevka airfield, Kaliningrad Oblast",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Voronezh_radar",
     "text": "Voronezh radar article, sites table"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Russian Aerospace Forces"
     }
    ],
    "status": "OPERATING",
    "note": "Fully operational 2014 per article"
   },
   {
    "slug": "voronezh-mishelevka",
    "name": "Voronezh-VP radar, Mishelevka (Irkutsk)",
    "abbr": "Voronezh-VP radar,.",
    "type": "MILITARY_BASE",
    "kind": "space tracking site",
    "country": "643",
    "lat": 52.85559,
    "lon": 103.23165,
    "precision": "Per-site coordinate in the Voronezh radar article table (52 51 20.11 N, 103 13 53.94 E), converted from DMS",
    "where": "Mishelevka, Irkutsk Oblast",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Voronezh_radar",
     "text": "Voronezh radar article, sites table"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Russian Aerospace Forces"
     }
    ],
    "status": "OPERATING",
    "note": "Fully operational since 2015 per article"
   },
   {
    "slug": "voronezh-yeniseysk",
    "name": "Voronezh-DM radar, Yeniseysk",
    "abbr": "Voronezh-DM radar,.",
    "type": "MILITARY_BASE",
    "kind": "space tracking site",
    "country": "643",
    "lat": 58.50611,
    "lon": 92.04611,
    "precision": "Per-site coordinate in the Voronezh radar article table (58 30 22 N, 92 02 46 E), converted from DMS",
    "where": "Near Yeniseysk, Krasnoyarsk Krai",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Voronezh_radar",
     "text": "Voronezh radar article, sites table"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Russian Aerospace Forces"
     }
    ],
    "status": "OPERATING",
    "note": "Fully operational December 2017 per article"
   },
   {
    "slug": "voronezh-vorkuta",
    "name": "Voronezh-M radar, Vorkuta",
    "abbr": "Voronezh-M radar,.",
    "type": "MILITARY_BASE",
    "kind": "space tracking site",
    "country": "643",
    "lat": 67.61397,
    "lon": 63.75542,
    "precision": "Per-site coordinate in the Voronezh radar article table (67 36 50.3 N, 63 45 19.5 E), converted from DMS",
    "where": "Near Vorkuta, Komi Republic",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Voronezh_radar",
     "text": "Voronezh radar article, sites table"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Russian Aerospace Forces"
     }
    ],
    "status": "UNDER_CONSTRUCTION",
    "note": "Article: construction started (2015 per a reference), completion expected 2022 per a 2019 MoD statement; later status not checked"
   },
   {
    "slug": "voronezh-olenegorsk",
    "name": "Voronezh-VP radar, Olenegorsk",
    "abbr": "Voronezh-VP radar,.",
    "type": "MILITARY_BASE",
    "kind": "space tracking site",
    "country": "643",
    "lat": 68.09056,
    "lon": 34.3275,
    "precision": "Per-site coordinate in the Voronezh radar article table (68 05 26 N, 34 19 39 E), converted from DMS",
    "where": "Near Olenegorsk, Murmansk Oblast",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Voronezh_radar",
     "text": "Voronezh radar article, sites table"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Russian Aerospace Forces"
     }
    ],
    "status": "UNDER_CONSTRUCTION",
    "note": "Article: construction started, completion expected 2022 per MoD; later status not checked"
   },
   {
    "slug": "daryal-pechora",
    "name": "Daryal radar, Pechora",
    "abbr": "Daryal radar, Pechora",
    "type": "MILITARY_BASE",
    "kind": "space tracking site",
    "country": "643",
    "lat": 65.21016,
    "lon": 57.29538,
    "precision": "Transmitter coordinate printed on the Daryal radar article (65 12 36.59 N, 57 17 43.38 E), converted from DMS; receiver about 1.1 km west at 65.21015, 57.27630",
    "where": "Pechora, Komi Republic",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Daryal_radar",
     "text": "Daryal radar article, Pechora entry"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Russian Aerospace Forces"
     }
    ],
    "status": "OPERATING",
    "note": "Table says in operation"
   },
   {
    "slug": "dnepr-mishelevka",
    "name": "Dnepr radar, Mishelevka",
    "abbr": "Dnepr radar, Mishelevka",
    "type": "MILITARY_BASE",
    "kind": "space tracking site",
    "country": "643",
    "lat": 52.8775,
    "lon": 103.27333,
    "precision": "Coordinate printed in the Dnepr radar article table for OS-1 Mishelevka (52 52 39 N, 103 16 24 E), converted from DMS",
    "where": "Mishelevka, Irkutsk Oblast",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Dnepr_radar",
     "text": "Dnepr radar article, sites table"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Russian Aerospace Forces"
     }
    ],
    "status": "OPERATING",
    "note": "Table says operational; the same table lists other Dnestr-era radars at Mishelevka, not included"
   },
   {
    "slug": "dnepr-olenegorsk",
    "name": "Dnestr-M radar, Olenegorsk-1",
    "abbr": "Dnestr-M radar,.",
    "type": "MILITARY_BASE",
    "kind": "space tracking site",
    "country": "643",
    "lat": 68.11417,
    "lon": 33.91028,
    "precision": "Coordinate in the Dnepr radar article table (68 06 51 N, 33 54 37 E; decimal 68.1141, 33.9102), converted from DMS",
    "where": "Olenegorsk, Kola Peninsula, Murmansk Oblast",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Dnepr_radar",
     "text": "Dnepr radar article, sites table (RO-1)"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Russian Aerospace Forces"
     }
    ],
    "status": "OPERATING",
    "note": "Corrected: the table lists the Olenegorsk-1 radar as Dnestr-M, operational, not Dnepr"
   },
   {
    "slug": "dnepr-balkhash",
    "name": "Dnepr radar, Balkhash (Sary Shagan)",
    "abbr": "Dnepr radar, Balkhash.",
    "type": "MILITARY_BASE",
    "kind": "space tracking site",
    "country": "398",
    "lat": 46.60306,
    "lon": 74.53111,
    "precision": "Coordinate in the Dnepr radar article table for the Dnepr row at Balkhash (46 36 11 N, 74 31 52 E), converted from DMS",
    "where": "Balkhash / Sary Shagan, Kazakhstan",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Dnepr_radar",
     "text": "Dnepr radar article, sites table"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Russian Aerospace Forces (site in Kazakhstan)"
     }
    ],
    "status": "DECOMMISSIONED",
    "statusDate": "2020",
    "note": "RETIRED. Table says decommissioned 2020"
   },
   {
    "slug": "volga-hantsavichy",
    "name": "Volga radar, Hantsavichy (Baranavichy)",
    "abbr": "Volga radar,.",
    "type": "MILITARY_BASE",
    "kind": "space tracking site",
    "country": "112",
    "lat": 52.83472,
    "lon": 26.475,
    "precision": "Infobox coordinate of the facility article (52 50 05 N, 26 28 30 E), converted from DMS; transmitter and receiver coordinates in the table are about 3 km apart",
    "where": "Hantsavichy, Brest Region, Belarus",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Volga_radar",
     "text": "Volga radar article"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Russian Aerospace Forces (leased site)"
     }
    ],
    "status": "OPERATING",
    "note": "Commissioned 1 October 2003; infobox says operational"
   },
   {
    "slug": "don-2n-sofrino",
    "name": "Don-2N radar, Sofrino",
    "abbr": "Don-2N radar, Sofrino",
    "type": "MILITARY_BASE",
    "kind": "space tracking site",
    "country": "643",
    "lat": 56.17333,
    "lon": 37.76917,
    "precision": "Coordinate on the Don-2N article (56 10 24 N, 37 46 09 E), converted from DMS",
    "where": "Near Sofrino, Pushkinsky District, Moscow Oblast",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Don-2N",
     "text": "Don-2N article"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Russian Aerospace Forces"
     }
    ],
    "status": "OPERATING",
    "note": "Infobox type reads early warning radar, missile defence, space surveillance; operational 1996"
   },
   {
    "slug": "dunay-3u-chekhov",
    "name": "Dunay-3U radar, Chekhov",
    "abbr": "Dunay-3U radar, Chekhov",
    "type": "MILITARY_BASE",
    "kind": "space tracking site",
    "country": "643",
    "lat": 55.20667,
    "lon": 37.29472,
    "precision": "Transmitter coordinate on the Dunay radar article table (55 12 24 N, 37 17 41 E), converted from DMS; receiver at 55.23111, 37.29694",
    "where": "Near Chekhov, Moscow Oblast",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Dunay_radar",
     "text": "Dunay radar article, Dunay-3U location table"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Russian Space Forces"
     }
    ],
    "status": "OPERATING",
    "statusDate": "article: sector 61 in use, life extension to December 2009, may have been extended again",
    "note": "Status is dated (about 2009); sector 62 decommissioned 1998"
   },
   {
    "slug": "krona-zelenchukskaya-radar",
    "name": "Krona radar site, Zelenchukskaya",
    "abbr": "Krona radar site,.",
    "type": "MILITARY_BASE",
    "kind": "space tracking site",
    "country": "643",
    "lat": 43.82611,
    "lon": 41.34306,
    "precision": "Radar installation coordinate on the Krona article (43 49 34 N, 41 20 35 E), converted from DMS; about 30 km from the optical site",
    "where": "Zelenchukskaya, Karachay-Cherkessia",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Krona_space_object_recognition_station",
     "text": "Krona article"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Russian Space Forces"
     }
    ],
    "status": "OPERATING",
    "note": "Operational since 1999 per article"
   },
   {
    "slug": "krona-chapal-optical",
    "name": "Krona optical and laser locator site, Chapal Mountain",
    "abbr": "Krona optical and laser.",
    "type": "OBSERVATION_STATION",
    "kind": "space tracking site",
    "country": "643",
    "lat": 43.71694,
    "lon": 41.23167,
    "precision": "Optical site coordinate on the Krona article (43 43 01 N, 41 13 54 E; decimal 43.7169171, 41.2316883), converted from DMS",
    "where": "Chapal Mountain above Zelenchukskaya, Karachay-Cherkessia",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Krona_space_object_recognition_station",
     "text": "Krona article"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Russian Space Forces"
     }
    ],
    "status": "OPERATING",
    "note": "Laser optical locator added 2005 per article"
   },
   {
    "slug": "krona-n-nakhodka",
    "name": "Krona-N radar, Nakhodka",
    "abbr": "Krona-N radar, Nakhodka",
    "type": "MILITARY_BASE",
    "kind": "space tracking site",
    "country": "643",
    "lat": 42.93583,
    "lon": 132.57667,
    "precision": "Coordinate on the Krona-N article (42 56 09 N, 132 34 36 E), converted from DMS",
    "where": "Near Nakhodka / Fokino-1, Primorsky Krai",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Krona-N",
     "text": "Krona-N article; status from the Krona article"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Russian Space Forces"
     }
    ],
    "status": "UNDER_CONSTRUCTION",
    "note": "Article: under construction; 2007 statement projected start 2008, no later announcement; no optical component"
   },
   {
    "slug": "altay-optical-laser-center",
    "name": "Altay Optical Laser Center",
    "abbr": "Altay Optical Laser.",
    "type": "OBSERVATION_STATION",
    "kind": "space tracking site",
    "country": "643",
    "lat": 51.2,
    "lon": 82.3,
    "precision": "ILRS station page value to one decimal degree (about 10 km), elevation 270 m; the station, not a surveyed point",
    "where": "Altai Krai",
    "cite": {
     "url": "https://ilrs.dgfi.tum.de/network/stations/active/ALTL_general.html",
     "text": "ILRS Altay station general page"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Institute for Precision Instrument Engineering (NPK SPP), Moscow"
     }
    ],
    "status": "OPERATING",
    "statusDate": "RussianSpaceWeb 2012-2013; the ILRS page shows no status field",
    "note": "Coordinate and elevation confirmed on the ILRS page; the nearby-town wording in the earlier draft was not seen on the page and is removed"
   },
   {
    "slug": "sazhen-komsomolsk",
    "name": "Komsomolsk-na-Amure laser station (Sazhen-S)",
    "abbr": "Komsomolsk-na-Amure.",
    "type": "OBSERVATION_STATION",
    "kind": "space tracking site",
    "country": "643",
    "lat": 50.69461,
    "lon": 136.74383,
    "precision": "ILRS station page coordinate, elevation 269.4 m",
    "where": "Komsomolsk-na-Amure, Khabarovsk Krai",
    "cite": {
     "url": "https://ilrs.dgfi.tum.de/network/stations/active/KOML_general.html",
     "text": "ILRS KOML station page; Sazhen-S per RussianSpaceWeb"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "OJC RPC PSI, Moscow"
     }
    ],
    "status": "OPERATING",
    "statusDate": "RussianSpaceWeb network list as of 2012; the ILRS page shows no status field",
    "note": "Coordinates confirmed on the ILRS page"
   },
   {
    "slug": "sazhen-arkhyz",
    "name": "Arkhyz laser station (Sazhen-TM-D)",
    "abbr": "Arkhyz laser station.",
    "type": "OBSERVATION_STATION",
    "kind": "space tracking site",
    "country": "643",
    "lat": 43.65,
    "lon": 41.4333,
    "precision": "ILRS station page coordinate, elevation 2077 m",
    "where": "Nizhniy Arkhyz, Karachay-Cherkessia",
    "cite": {
     "url": "https://ilrs.dgfi.tum.de/network/stations/active/ARKL_general.html",
     "text": "ILRS ARKL station page; Sazhen-TM-D per RussianSpaceWeb"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "OJC RPC PSI, Moscow"
     }
    ],
    "status": "OPERATING",
    "statusDate": "RussianSpaceWeb 2012 list and a January 2013 use; the ILRS page shows no status field",
    "note": "Coordinates confirmed on the ILRS page"
   }
  ],
  "sensors": [
   {
    "id": "voronezh-dm-pionersky",
    "site": "voronezh-pionersky",
    "name": "Voronezh-DM",
    "type": "PHASED_ARRAY_EARLY_WARNING",
    "emission": "ACTIVE",
    "status": "OPERATING",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Voronezh_radar",
     "text": "Voronezh radar article"
    },
    "note": ""
   },
   {
    "id": "voronezh-vp-mishelevka",
    "site": "voronezh-mishelevka",
    "name": "Voronezh-VP",
    "type": "PHASED_ARRAY_EARLY_WARNING",
    "emission": "ACTIVE",
    "status": "OPERATING",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Voronezh_radar",
     "text": "Voronezh radar article"
    },
    "note": ""
   },
   {
    "id": "voronezh-dm-yeniseysk",
    "site": "voronezh-yeniseysk",
    "name": "Voronezh-DM",
    "type": "PHASED_ARRAY_EARLY_WARNING",
    "emission": "ACTIVE",
    "status": "OPERATING",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Voronezh_radar",
     "text": "Voronezh radar article"
    },
    "note": ""
   },
   {
    "id": "voronezh-m-vorkuta",
    "site": "voronezh-vorkuta",
    "name": "Voronezh-M",
    "type": "PHASED_ARRAY_EARLY_WARNING",
    "emission": "ACTIVE",
    "band": {
     "name": "VHF band; the article infobox gives 150-200 MHz for the Voronezh-M variant (variant-level, not a limit for this site)",
     "designation": ""
    },
    "status": "UNDER_CONSTRUCTION",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Voronezh_radar",
     "text": "Voronezh radar article"
    },
    "note": ""
   },
   {
    "id": "voronezh-vp-olenegorsk",
    "site": "voronezh-olenegorsk",
    "name": "Voronezh-VP",
    "type": "PHASED_ARRAY_EARLY_WARNING",
    "emission": "ACTIVE",
    "status": "UNDER_CONSTRUCTION",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Voronezh_radar",
     "text": "Voronezh radar article"
    },
    "note": ""
   },
   {
    "id": "daryal-pechora",
    "site": "daryal-pechora",
    "name": "Daryal",
    "type": "PHASED_ARRAY_EARLY_WARNING",
    "emission": "ACTIVE",
    "band": {
     "name": "VHF band, 150-200 MHz as published for the Daryal radar",
     "designation": "",
     "lowMHz": 150,
     "highMHz": 200
    },
    "status": "OPERATING",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Daryal_radar",
     "text": "Daryal radar article (infobox and text: 150 to 200 MHz)"
    },
    "note": ""
   },
   {
    "id": "dnepr-mishelevka",
    "site": "dnepr-mishelevka",
    "name": "Dnepr",
    "type": "PHASED_ARRAY_EARLY_WARNING",
    "emission": "ACTIVE",
    "band": {
     "name": "VHF band, 154-162 MHz as published in the article infobox for the Dnestr/Dnepr radar family",
     "designation": "",
     "lowMHz": 154,
     "highMHz": 162
    },
    "status": "OPERATING",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Dnepr_radar",
     "text": "Dnepr radar article"
    },
    "note": ""
   },
   {
    "id": "dnepr-olenegorsk",
    "site": "dnepr-olenegorsk",
    "name": "Dnestr-M",
    "type": "PHASED_ARRAY_EARLY_WARNING",
    "emission": "ACTIVE",
    "band": {
     "name": "VHF band, 154-162 MHz as published in the article infobox for the Dnestr/Dnepr radar family",
     "designation": "",
     "lowMHz": 154,
     "highMHz": 162
    },
    "status": "OPERATING",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Dnepr_radar",
     "text": "Dnepr radar article, sites table (type Dnestr-M)"
    },
    "note": "Renamed from Dnepr after check"
   },
   {
    "id": "dnepr-balkhash",
    "site": "dnepr-balkhash",
    "name": "Dnepr",
    "type": "PHASED_ARRAY_EARLY_WARNING",
    "emission": "ACTIVE",
    "band": {
     "name": "VHF band, 154-162 MHz as published in the article infobox for the Dnestr/Dnepr radar family",
     "designation": "",
     "lowMHz": 154,
     "highMHz": 162
    },
    "status": "retired",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Dnepr_radar",
     "text": "Dnepr radar article"
    },
    "note": "RETIRED."
   },
   {
    "id": "volga-hantsavichy",
    "site": "volga-hantsavichy",
    "name": "Volga",
    "type": "PHASED_ARRAY_EARLY_WARNING",
    "emission": "ACTIVE",
    "status": "OPERATING",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Volga_radar",
     "text": "Volga radar article"
    },
    "note": ""
   },
   {
    "id": "don-2n",
    "site": "don-2n-sofrino",
    "name": "Don-2N",
    "type": "PHASED_ARRAY_EARLY_WARNING",
    "emission": "ACTIVE",
    "status": "OPERATING",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Don-2N",
     "text": "Don-2N article (infobox lists space surveillance among its types)"
    },
    "note": ""
   },
   {
    "id": "dunay-3u",
    "site": "dunay-3u-chekhov",
    "name": "Dunay-3U",
    "type": "PHASED_ARRAY_EARLY_WARNING",
    "emission": "ACTIVE",
    "powerW": 500000,
    "status": "OPERATING",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Dunay_radar",
     "text": "Dunay radar article"
    },
    "note": "500 kW since 2003 (reduced from 1,800 kW) per article; one sector only; status dated about 2009"
   },
   {
    "id": "krona-uhf",
    "site": "krona-zelenchukskaya-radar",
    "name": "Krona UHF radar",
    "type": "PHASED_ARRAY_EARLY_WARNING",
    "emission": "ACTIVE",
    "status": "OPERATING",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Krona_space_object_recognition_station",
     "text": "Krona article"
    },
    "note": "20 m x 20 m antenna"
   },
   {
    "id": "krona-shf",
    "site": "krona-zelenchukskaya-radar",
    "name": "Krona SHF radar",
    "type": "RADAR_PULSE",
    "emission": "ACTIVE",
    "status": "OPERATING",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Krona_space_object_recognition_station",
     "text": "Krona article"
    },
    "note": ""
   },
   {
    "id": "krona-laser-locator",
    "site": "krona-chapal-optical",
    "name": "Krona laser optical locator",
    "type": "LASER_RANGEFINDER",
    "emission": "ACTIVE",
    "status": "OPERATING",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Krona_space_object_recognition_station",
     "text": "Krona article"
    },
    "note": "Added 2005 per article (cites a 2005 news report)"
   },
   {
    "id": "krona-n-uhf",
    "site": "krona-n-nakhodka",
    "name": "Krona-N UHF radar",
    "type": "PHASED_ARRAY_EARLY_WARNING",
    "emission": "ACTIVE",
    "status": "UNDER_CONSTRUCTION",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Krona-N",
     "text": "Krona-N article"
    },
    "note": ""
   },
   {
    "id": "krona-n-shf",
    "site": "krona-n-nakhodka",
    "name": "Krona-N SHF radar",
    "type": "RADAR_PULSE",
    "emission": "ACTIVE",
    "status": "UNDER_CONSTRUCTION",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Krona-N",
     "text": "Krona-N article"
    },
    "note": ""
   },
   {
    "id": "altay-laser-0p6",
    "site": "altay-optical-laser-center",
    "name": "Altay laser telescope (0.6 m, 2004)",
    "type": "LASER_RANGEFINDER",
    "emission": "ACTIVE",
    "apertureM": 0.6,
    "status": "OPERATING",
    "cite": {
     "url": "https://mail.russianspaceweb.com/kik_laser.html",
     "text": "RussianSpaceWeb laser stations page"
    },
    "note": "0.6 m mirror introduced 2004; the page calls it the predecessor of the 3.12 m instrument, so current use is uncertain (2012-13 source)"
   },
   {
    "id": "altay-3p12",
    "site": "altay-optical-laser-center",
    "name": "Altay 3.12 m optical and laser telescope",
    "type": "OPTICAL_TELESCOPE",
    "emission": "PASSIVE",
    "apertureM": 3.12,
    "status": "UNDER_CONSTRUCTION",
    "cite": {
     "url": "https://mail.russianspaceweb.com/kik_laser.html",
     "text": "RussianSpaceWeb laser stations page"
    },
    "note": "Page: scheduled for completion in 2014; completion not confirmed"
   },
   {
    "id": "sazhen-s-komsomolsk",
    "site": "sazhen-komsomolsk",
    "name": "Sazhen-S",
    "type": "LASER_RANGEFINDER",
    "emission": "ACTIVE",
    "status": "OPERATING",
    "cite": {
     "url": "https://mail.russianspaceweb.com/kik_laser.html",
     "text": "RussianSpaceWeb laser stations page (2012 status)"
    },
    "note": "Status as of 2012 per page"
   },
   {
    "id": "sazhen-tm-d-arkhyz",
    "site": "sazhen-arkhyz",
    "name": "Sazhen-TM-D",
    "type": "LASER_RANGEFINDER",
    "emission": "ACTIVE",
    "status": "OPERATING",
    "cite": {
     "url": "https://mail.russianspaceweb.com/kik_laser.html",
     "text": "RussianSpaceWeb laser stations page (2012 status)"
    },
    "note": "Status as of 2012; used January 2013 per page"
   }
  ],
  "inorbit": [],
  "leftOut": [
   [
    "Voronezh at Pechora",
    "Article says one was planned for 2015 to replace the Daryal; no coordinates or status given"
   ],
   [
    "Usolye-Sibirskoye radar",
    "Not named in the Voronezh article; the Irkutsk entry is Mishelevka, already included"
   ],
   [
    "Okno-S, Primorsky Krai",
    "Article says no official confirmation and gives conflicting coordinate estimates"
   ],
   [
    "Pritsel",
    "No page found describing it"
   ],
   [
    "Sazhen-T at Shelkovo, Sazhen-TOS at Baikonur, Sazhen-TM at Svetloe",
    "No coordinates read for these stations"
   ],
   [
    "Sevastopol Dnepr/Voronezh, Mukachevo, Gabala Daryal, Skrunda",
    "Closed, demolished or planned on the pages read"
   ],
   [
    "Moment (movable) station",
    "No coordinates"
   ]
  ],
  "nowPublished": [
   "Krona, Krona-N"
  ]
 },
 "eiscat": {
  "sites": [
   {
    "slug": "eiscat-svalbard-no",
    "name": "EISCAT Svalbard Radar",
    "abbr": "EISCAT Svalbard Radar",
    "type": "RESEARCH_FACILITY",
    "kind": "space tracking site",
    "country": "578",
    "lat": 78.153056,
    "lon": 16.028889,
    "precision": "EISCAT page prints 78 09 11 N 16 01 44 E for the radar, written as decimal; facility coordinate",
    "where": "Breinosa near Longyearbyen, Spitsbergen, Svalbard",
    "cite": {
     "url": "https://eiscat.se/about/sites/eiscat-svalbard-radar",
     "text": "78 09 11 N 16 01 44 E; Breinosa, Gruve7-veien, Longyearbyen"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "EISCAT Scientific Association"
     }
    ],
    "status": "UNVERIFIED",
    "note": "Page gives no status date and does not mention space debris use."
   }
  ],
  "sensors": [
   {
    "id": "eiscat-esr-32m",
    "site": "eiscat-svalbard-no",
    "name": "EISCAT Svalbard Radar 32 m dish",
    "type": "RADAR_PULSE",
    "emission": "ACTIVE",
    "apertureM": 32,
    "status": "UNVERIFIED",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/EISCAT",
     "text": "Svalbard radar operates in the UHF band at 500 MHz with a 32 m steerable dish and a 42 m fixed dish"
    },
    "note": "Incoherent scatter research radar; no source opened states a routine space-object tracking role."
   }
  ],
  "inorbit": [],
  "leftOut": [
   [
    "EISCAT Svalbard 42 m fixed dish",
    "Fixed along the magnetic field; not a separate tracking row"
   ],
   [
    "EISCAT Svalbard Radar, Longyearbyen",
    "Wikipedia EISCAT lacks its coordinates; 78 09 11 N 16 01 44 E appeared only in a search excerpt of an EISCAT page that I could not open; 500 MHz, 1000 kW peak, 32 m and 42 m dishes per Wikipedia"
   ],
   [
    "EISCAT_3D (Skibotn, Karesuvanto, Kaiseniemi)",
    "no per-site coordinates found; Wikipedia says construction began Nov 2022, status not stated"
   ]
  ]
 },
 "eu-sst": {
  "sites": [
   {
    "slug": "gestra-koblenz-de",
    "name": "GESTRA Schmidtenhoehe",
    "abbr": "GESTRA Schmidtenhoehe",
    "type": "MILITARY_BASE",
    "kind": "space tracking site",
    "country": "276",
    "lat": 50.336927,
    "lon": 7.6386,
    "precision": "Wikipedia coordinate of the Bundeswehr training area Koblenz-Schmidtenhoehe where the transmit and receive units stand; exact units not separately published",
    "where": "Standortuebungsplatz Koblenz-Schmidtenhoehe, Koblenz",
    "cite": {
     "url": "https://de.wikipedia.org/wiki/German_Experimental_Space_Surveillance_and_Tracking_Radar",
     "text": "50 20 12.9 N 7 38 19 E; handed over September 2020, operations mid-2021"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Weltraumlagezentrum / DLR"
     }
    ],
    "status": "OPERATING",
    "statusDate": "2021",
    "note": ""
   },
   {
    "slug": "s3tsr-moron-es",
    "name": "S3TSR Moron Air Base",
    "abbr": "S3TSR Moron Air Base",
    "type": "MILITARY_BASE",
    "kind": "space tracking site",
    "country": "724",
    "lat": 37.17472,
    "lon": -5.61583,
    "precision": "Wikipedia coordinate of Moron Air Base; radar is on the base, exact spot not published",
    "where": "Moron de la Frontera, Seville",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Moron_Air_Base",
     "text": "37 10 29 N 05 36 57 W (base coordinate). The radar at the Moron base is stated on."
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Spanish Air and Space Force"
     }
    ],
    "status": "OPERATING",
    "statusDate": "2019",
    "note": "Indra page (https://www.indragroup.com/en/news/space-surveillance-radar-developed-indra-first-europe-detect-fragments-satellite-destroyed) says radar at Moron base is operational (2019)."
   },
   {
    "slug": "cheia-radar-ro",
    "name": "Cheia SST radar",
    "abbr": "Cheia SST radar",
    "type": "SATELLITE_GROUND_STATION",
    "kind": "space tracking site",
    "country": "642",
    "lat": 45.456667,
    "lon": 25.946667,
    "alt": 900,
    "precision": "coordinates printed in the paper for the antennas (45 27 24 N 25 56 48 E, about 900 m), written as decimal",
    "where": "Cheia Satellite Communication Center, Prahova County",
    "cite": {
     "url": "https://pmc.ncbi.nlm.nih.gov/articles/PMC9105499",
     "text": "latitude 45 27 24 N, longitude 25 56 48 E at 900 m altitude"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Romanian Space Agency (runs the national SST operational centre; the paper does not name the radar operator)"
     }
    ],
    "status": "UNVERIFIED",
    "statusDate": "2022",
    "note": "Paper says commissioning expected first half of 2022."
   }
  ],
  "sensors": [
   {
    "id": "gestra-radar",
    "site": "gestra-koblenz-de",
    "name": "GESTRA",
    "type": "RADAR_AESA",
    "emission": "ACTIVE",
    "status": "OPERATING",
    "cite": {
     "url": "https://de.wikipedia.org/wiki/German_Experimental_Space_Surveillance_and_Tracking_Radar",
     "text": "Described as a phased array radar"
    },
    "note": "German page calls it a fully digital phased-array system; active or passive element type not stated, AESA label is a best fit."
   },
   {
    "id": "s3tsr-radar",
    "site": "s3tsr-moron-es",
    "name": "S3TSR",
    "type": "RADAR_PULSE",
    "emission": "ACTIVE",
    "status": "OPERATING",
    "cite": {
     "url": "https://www.indragroup.com/en/news/space-surveillance-radar-developed-indra-first-europe-detect-fragments-satellite-destroyed",
     "text": "Operational radar at Moron base"
    },
    "note": "Indra page opened gives no radar type or band; RADAR_PULSE is a best-fit label, not stated by a source."
   },
   {
    "id": "cheia-sst-radar",
    "site": "cheia-radar-ro",
    "name": "Cheia C band radar",
    "type": "RADAR_CW",
    "emission": "ACTIVE",
    "band": {
     "name": "C band; design band 5830-5850 MHz per paper",
     "designation": "",
     "lowMHz": 5830,
     "highMHz": 5850
    },
    "powerW": 2500,
    "apertureM": 32,
    "status": "UNVERIFIED",
    "cite": {
     "url": "https://pmc.ncbi.nlm.nih.gov/articles/PMC9105499",
     "text": "Two 32 m antennas, 2.5 kW design average CW power, 5830-5850 MHz"
    },
    "note": "One section of the paper misprints GHz; design power, initial tests at 125 W in December 2021."
   }
  ],
  "inorbit": [],
  "leftOut": [
   [
    "BIRALES (Salto di Quirra transmitter, Medicina receiver)",
    "Role and sites only seen in search listings of INAF papers; Northern Cross Wikipedia page returned 404; Salto di Quirra tag (39 31 38 N 9 37 58 E) opened but its link to BIRALES not confirmed on an opened page"
   ],
   [
    "Chilbolton Observatory",
    "Page (51 08 40 N 1 26 19 W) does not mention space surveillance"
   ],
   [
    "GESTRA at Schmidtenhoehe, Koblenz",
    "no source opened publishes facility coordinates; Fraunhofer/DLR pages place it at Schmidtenhoehe. L-band 1280-1380 MHz was reported in a search excerpt of a DLR/FHR document (not opened); inaugurated 13 Oct 2020"
   ]
  ],
  "nowPublished": [
   "S3TSR (Spain), Moron AB",
   "GESTRA (Germany), Schmidtenhoehe, Koblenz"
  ]
 },
 "esa-ogs": {
  "sites": [
   {
    "slug": "esa-izana-1",
    "name": "ESA Izana-1 laser ranging station (ILRS IZ1L)",
    "abbr": "ESA Izana-1 laser.",
    "type": "OBSERVATION_STATION",
    "kind": "space tracking site",
    "country": "724",
    "lat": 28.29959791,
    "lon": -16.5106116,
    "alt": 2429.662,
    "precision": "ILRS station coordinates for the laser station itself",
    "where": "Teide Observatory, Tenerife, Spain",
    "cite": {
     "url": "https://ilrs.cddis.eosdis.nasa.gov/network/stations/active/IZ1L_station_info.html",
     "text": "ILRS IZ1L station info, listed under active stations"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "ESA"
     }
    ],
    "status": "OPERATING",
    "statusDate": "2026-06",
    "note": "Listed under ILRS active stations; site log revision 4 dated 2026-06-25. Debris daytime ranging upgrade claim not verified."
   }
  ],
  "sensors": [
   {
    "id": "esa-izana-1-slr",
    "site": "esa-izana-1",
    "name": "Izana-1 SLR (0.8 m receive)",
    "type": "LASER_RANGEFINDER",
    "emission": "ACTIVE",
    "apertureM": 0.8,
    "status": "OPERATING",
    "cite": {
     "url": "https://ilrs.cddis.eosdis.nasa.gov/network/stations/active/IZ1L_station_info.html",
     "text": "0.8 m receiving Ritchey-Chretien; 0.075 m transmit"
    },
    "note": "ILRS site log: 0.8 m receiving Ritchey-Chretien, 0.075 m transmitting Galilei"
   }
  ],
  "inorbit": [],
  "leftOut": [
   [
    "ESA Flyeye (Monte Mufara)",
    "Wikipedia article gives no coordinates; first light 20 May 2025 (Flyeye-1)"
   ],
   [
    "ESA Test-Bed Telescope Cebreros (Z58) and La Silla (W57)",
    "NEOCC page shows coordinates (40.454075, -4.369319; -29.25524, -70.73905) and 0.56 m aperture but states no operating status, and no space surveillance role; status cannot be sourced"
   ]
  ]
 }
};

// Networks first published in round 2: one provider each (`sdn-ssa-<slug>`).
export const NEW_NETWORKS = [
 {
  "slug": "ison",
  "name": "International Scientific Optical Network (ISON)",
  "operator": "Keldysh Institute of Applied Mathematics, Russian Academy of Sciences",
  "kind": "scientific-optical",
  "status": "active",
  "terms": TERMS_WP,
  "credit": "Wikipedia contributors (CC BY-SA 4.0) and the pages cited per row",
  "sites": [
   {
    "slug": "ison-ussuriysk",
    "name": "Ussuriysk Astrophysical Observatory",
    "abbr": "Ussuriysk Astrophysical.",
    "type": "ASTRONOMICAL_OBSERVATORY",
    "kind": "space tracking site",
    "country": "643",
    "lat": 43.69944,
    "lon": 132.16333,
    "precision": "Coordinate printed on the observatory's own Wikipedia article, DMS 43 41 58 N 132 09 48 E (decimal 43.69944, 132.16333)",
    "where": "Gornotaeznoe, near Ussuriysk, Primorsky Krai",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Ussuriysk_Astrophysical_Observatory",
     "text": "Wikipedia article; ISON membership and VT-40/500 from the SDC5 paper 131 instrument table"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Far Eastern Branch of the Russian Academy of Sciences"
     }
    ],
    "status": "OPERATING",
    "statusDate": "The article gives no explicit status (regular observations since 1954); ISON membership per a paper of about 2009",
    "note": "Status rests on dated sources"
   },
   {
    "slug": "ison-abastumani",
    "name": "Abastumani Astrophysical Observatory",
    "abbr": "Abastumani.",
    "type": "ASTRONOMICAL_OBSERVATORY",
    "kind": "space tracking site",
    "country": "268",
    "lat": 41.7542,
    "lon": 42.8194,
    "precision": "Coordinate printed on the observatory's own Wikipedia article (41 45 15 N, 42 49 10 E)",
    "where": "Abastumani, Georgia",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Abastumani_Astrophysical_Observatory",
     "text": "Wikipedia article; ISON membership and AS-32 from the SDC5 paper 131 instrument table"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Georgian National Astrophysical Observatory (GENAO)"
     }
    ],
    "status": "OPERATING",
    "statusDate": "The article describes present-tense research; ISON membership per a paper of about 2009",
    "note": "Operator corrected to the name on the page; the earlier university name was not stated there"
   }
  ],
  "sensors": [
   {
    "id": "ison-ussuriysk-vt40",
    "site": "ison-ussuriysk",
    "name": "VT-40/500",
    "type": "OPTICAL_TELESCOPE",
    "emission": "PASSIVE",
    "apertureM": 0.5,
    "status": "OPERATING",
    "cite": {
     "url": "https://conference.sdo.esoc.esa.int/proceedings/sdc5/paper/131/SDC5-paper131.pdf",
     "text": "ISON instrument table, SDC5 paper 131 (about 2009)"
    },
    "note": "Aperture 50 cm confirmed in the paper's table; status as of about 2009"
   },
   {
    "id": "ison-abastumani-as32",
    "site": "ison-abastumani",
    "name": "AS-32",
    "type": "OPTICAL_TELESCOPE",
    "emission": "PASSIVE",
    "apertureM": 0.7,
    "status": "OPERATING",
    "cite": {
     "url": "https://conference.sdo.esoc.esa.int/proceedings/sdc5/paper/131/SDC5-paper131.pdf",
     "text": "ISON instrument table, SDC5 paper 131 (about 2009)"
    },
    "note": "Aperture 70 cm confirmed in the paper's table; status as of about 2009"
   }
  ],
  "inorbit": [],
  "leftOut": [
   [
    "Andrushivka (Ukraine), S-600 60 cm",
    "Coordinates on its Wikipedia article, but no operating status and a private observatory; not re-opened in this check"
   ],
   [
    "Terskol (Zeiss-2000 and K-800)",
    "Only a conference-slide coordinate seen in a search snippet, not an opened page"
   ],
   [
    "ISON-Kislovodsk (code D00), Mayaki, Tiraspol, Kitab, Tarija, Zimmerwald, Mayhill and others",
    "No coordinates read on an opened page"
   ]
  ]
 },
 {
  "slug": "china-pla",
  "name": "China PLA large phased array radars",
  "operator": "PLA (reported 63610 Unit for Korla; otherwise unstated)",
  "kind": "military radar network",
  "status": "partial",
  "terms": TERMS_UNSTATED,
  "credit": "the pages cited per row",
  "sites": [
   {
    "slug": "yiyuan-lpar",
    "name": "Yiyuan LPAR site, Shandong",
    "abbr": "Yiyuan LPAR site,.",
    "type": "MILITARY_BASE",
    "kind": "space tracking site",
    "country": "156",
    "lat": 36.025,
    "lon": 118.091944,
    "precision": "Published as 36 deg 01 min 30 sec N, 118 deg 05 min 31 sec E, written here as decimal degrees. Defense News attaches it to the site's FIRST large phased array radar on a mountaintop;",
    "where": "Mountaintop site in Yiyuan County, Shandong, about 70 miles southeast of Jinan",
    "cite": {
     "url": "https://www.defensenews.com/global/asia-pacific/2022/04/18/new-chinese-radar-looks-towards-japan-satellite-image-shows/",
     "text": "Defense News, 18 Apr 2022: site coordinates given in the article"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "PLA (unit not stated in the source)"
     }
    ],
    "status": "OPERATING",
    "statusDate": "2013",
    "note": "Status basis: first array described as completed around 2013-2014 (Google Earth imagery per the source); operational status of the newer array (built after Nov 2019) is not confirmed."
   },
   {
    "slug": "korla-lpar",
    "name": "Korla LPAR, Xinjiang",
    "abbr": "Korla LPAR, Xinjiang",
    "type": "MILITARY_BASE",
    "kind": "space tracking site",
    "country": "156",
    "lat": 41.641794,
    "lon": 86.237575,
    "precision": "Published as 41 deg 38 min 30.46 sec N, 86 deg 14 min 15.27 sec E, written as decimal degrees. The page credits an analyst with identifying the radar in 2009. Coordinate is for the radar as identified by the analysts.",
    "where": "Korla Missile Test Complex area, near Korla, Xinjiang",
    "cite": {
     "url": "https://www.armscontrolwonk.com/archive/605030/the-korla-missile-test-complex-revisited/",
     "text": "Arms Control Wonk, 'The Korla Missile Test Complex revisited' (March 2015): coordinates and imagery notes"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "PLA (unit not stated on the page; sources disagree)"
     }
    ],
    "status": "UNVERIFIED",
    "statusDate": "2013",
    "note": "Page shows imagery from January 2013 and October 2013 at different angles, so the array turns. Current operating status not stated."
   }
  ],
  "sensors": [
   {
    "id": "yiyuan-lpar-1",
    "site": "yiyuan-lpar",
    "name": "Yiyuan LPAR (first array, oriented south-southeast)",
    "type": "PHASED_ARRAY_EARLY_WARNING",
    "emission": "ACTIVE",
    "status": "completed ~2013-2014 per source; current operation not confirmed",
    "cite": {
     "url": "https://www.defensenews.com/global/asia-pacific/2022/04/18/new-chinese-radar-looks-towards-japan-satellite-image-shows/",
     "text": "Defense News: first LPAR completed around 2013-2014; second array built after Nov 2019 (seen Feb 2022 imagery)"
    },
    "note": "The source describes ballistic missile early warning and says LPARs can also be used for satellite tracking and general space surveillance; it does not state that this array does so."
   },
   {
    "id": "korla-lpar-1",
    "site": "korla-lpar",
    "name": "Korla large phased array radar",
    "type": "PHASED_ARRAY_EARLY_WARNING",
    "emission": "ACTIVE",
    "status": "UNVERIFIED",
    "cite": {
     "url": "https://www.armscontrolwonk.com/archive/605030/the-korla-missile-test-complex-revisited/",
     "text": "Arms Control Wonk: author says an LPAR would be useful for looking at missile intercept tests; the page does not confirm its role"
    },
    "note": "The page does not state a space surveillance role, band or operating status."
   }
  ],
  "inorbit": [],
  "leftOut": [
   [
    "Yiyuan second (newer) LPAR array, northeast-facing",
    "Same mountaintop site as the first array; no separate coordinates, band or confirmed status published."
   ],
   [
    "Korla LPAR, Xinjiang",
    "Named by Defense News and in a search result (GlobalSecurity, whose page returned HTTP 403 and was not opened); no coordinates, band or status read from an opened page."
   ],
   [
    "Lin'an LPAR, Zhejiang",
    "Named by Defense News as an LPAR site covering Japan, Korea and Taiwan; no coordinates on an opened page. A 4,000 km X-band claim came only from a search snippet of a secondary outlet."
   ],
   [
    "Heilongjiang (Huanan/Shuangyashan) LPAR",
    "Named only as 'Heilongjiang Province' in Defense News; no site name or coordinates found."
   ],
   [
    "Over-the-horizon radars (Wikipedia notes unspecified OTH-B and OTH-SW sets in China, one in Inner Mongolia)",
    "No named sites, coordinates or limits published; not space surveillance sensors on any opened page."
   ],
   [
    "Lin'an (Zhejiang), Huanan (Heilongjiang) and Hainan arrays",
    "No coordinates on any opened page."
   ],
   [
    "Second Yiyuan array",
    "No separate coordinates found."
   ],
   [
    "GlobalSecurity Korla coordinates",
    "Page returned 403; value seen only in a search snippet."
   ]
  ]
 },
 {
  "slug": "china-cas",
  "name": "Chinese observatories and laser ranging stations",
  "operator": "Chinese Academy of Sciences (Purple Mountain Observatory; Changchun Observatory, NAOC)",
  "kind": "scientific optical and laser ranging",
  "status": "partial",
  "terms": TERMS_UNSTATED,
  "credit": "the pages cited per row",
  "sites": [
   {
    "slug": "xuyi-cneos",
    "name": "Xuyi Observation Station (CNEOS telescope), Purple Mountain Observatory",
    "abbr": "Xuyi Observation.",
    "type": "ASTRONOMICAL_OBSERVATORY",
    "kind": "space tracking site",
    "country": "156",
    "lat": 32.7,
    "lon": 118.5,
    "alt": 180.9,
    "precision": "Published in a 2007 PMO debris paper (SDC5) as longitude 118.5 E, latitude 32.7 N, altitude 180.9 m for the CNEOS telescope dome, rounded to 0.1 degree. The paper's value is for the telescope, but only to one decimal place.",
    "where": "Xuyi, Jiangsu",
    "cite": {
     "url": "https://conference.sdo.esoc.esa.int/proceedings/sdc5/paper/3/SDC5-paper3.pdf",
     "text": "ESA SDC5 paper 3 (PMO, March 2007 GEO debris observations): telescope location and 1.04 m Schmidt aperture; text read with pdftotext"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Purple Mountain Observatory, Chinese Academy of Sciences"
     }
    ],
    "status": "OPERATING",
    "statusDate": "2006",
    "note": "Wikipedia Purple Mountain Observatory page says Chinese Near-Earth Object Survey observing at Xuyi since 2006 (MPC code D29). Current status not re-verified."
   },
   {
    "slug": "changchun-slr",
    "name": "Changchun SLR station (Changchun Observatory, NAOC)",
    "abbr": "Changchun SLR station.",
    "type": "ASTRONOMICAL_OBSERVATORY",
    "kind": "space tracking site",
    "country": "156",
    "lat": 43.7905,
    "lon": 125.4434,
    "alt": 274.9,
    "precision": "ILRS station information page, decimal degrees as published (43.7905 N, 125.4434 E, 274.9 m).",
    "where": "Changchun, Jilin",
    "cite": {
     "url": "https://ilrs.gsfc.nasa.gov/network/stations/active/CHAL_station_info.html",
     "text": "ILRS CHAL station page: coordinates, 0.60 m receive telescope, listed active"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Changchun Observatory, National Astronomical Observatories, Chinese Academy of Sciences"
     }
    ],
    "status": "OPERATING",
    "note": "Listed under ILRS active stations."
   },
   {
    "slug": "shanghai-sheshan-slr",
    "name": "Shanghai SLR station (Sheshan)",
    "abbr": "Shanghai SLR station.",
    "type": "ASTRONOMICAL_OBSERVATORY",
    "kind": "space tracking site",
    "country": "156",
    "lat": 31.0961,
    "lon": 121.1866,
    "alt": 99.961,
    "precision": "ILRS station page decimal degrees as printed, for the station on Sheshan Hill.",
    "where": "Sheshan Hill, Shanghai",
    "cite": {
     "url": "https://ilrs.gsfc.nasa.gov/network/stations/active/SHA2_station_info.html",
     "text": "ILRS SHA2 station page"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Shanghai Observatory (as printed)"
     }
    ],
    "status": "OPERATING",
    "note": "Listed in ILRS active stations."
   },
   {
    "slug": "kunming-slr",
    "name": "Kunming SLR station",
    "abbr": "Kunming SLR station",
    "type": "ASTRONOMICAL_OBSERVATORY",
    "kind": "space tracking site",
    "country": "156",
    "lat": 25.0298,
    "lon": 102.7977,
    "alt": 1987.05,
    "precision": "ILRS station page decimal degrees as printed, for the station.",
    "where": "Yunnan Province (ILRS name Kunming)",
    "cite": {
     "url": "https://ilrs.gsfc.nasa.gov/network/stations/active/KUN2_station_info.html",
     "text": "ILRS KUN2 station page"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "not read on the opened page"
     }
    ],
    "status": "OPERATING",
    "note": "Listed in ILRS active stations; site log dated January 2017."
   },
   {
    "slug": "wuhan-jiufeng-slr",
    "name": "Wuhan SLR station (JiuFeng)",
    "abbr": "Wuhan SLR station.",
    "type": "RESEARCH_FACILITY",
    "kind": "space tracking site",
    "country": "156",
    "lat": 30.515667,
    "lon": 114.490167,
    "alt": 76.7,
    "precision": "ILRS station page decimal degrees as printed, for the station.",
    "where": "Jiufeng hill, southeast of Wuhan, Hubei",
    "cite": {
     "url": "https://ilrs.gsfc.nasa.gov/network/stations/active/JFNL_station_info.html",
     "text": "ILRS JFNL station page"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "APM, CAS (as printed)"
     }
    ],
    "status": "OPERATING",
    "note": "Listed in ILRS active stations; site log updated 2026-05-20."
   },
   {
    "slug": "beijing-slr",
    "name": "Beijing SLR station",
    "abbr": "Beijing SLR station",
    "type": "RESEARCH_FACILITY",
    "kind": "space tracking site",
    "country": "156",
    "lat": 39.6069,
    "lon": 115.892,
    "alt": 82.3,
    "precision": "ILRS station page decimal degrees as printed, for the station.",
    "where": "Fangshan district, Beijing",
    "cite": {
     "url": "https://ilrs.gsfc.nasa.gov/network/stations/active/BEIL_station_info.html",
     "text": "ILRS BEIL station page"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Chinese Academy of Surveying and Mapping (as printed)"
     }
    ],
    "status": "OPERATING",
    "note": "Listed in ILRS active stations; site log dated 2025-11-20."
   },
   {
    "slug": "xian-ntsc-slr",
    "name": "Xi'an SLR station (National Time Service Center)",
    "abbr": "Xi'an SLR station.",
    "type": "RESEARCH_FACILITY",
    "kind": "space tracking site",
    "country": "156",
    "lat": 34.3517,
    "lon": 109.2141,
    "alt": 940.297,
    "precision": "ILRS station page decimal degrees as printed, for the station; page says it is on top of Lishan mountain.",
    "where": "Lishan mountain, Xi'an, Shaanxi",
    "cite": {
     "url": "https://ilrs.gsfc.nasa.gov/network/stations/active/NTSC_station_info.html",
     "text": "ILRS NTSC station page"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "National Time Service Center, CAS"
     }
    ],
    "status": "OPERATING",
    "note": "Listed in ILRS active stations. Not the Xi'an Satellite Control Center."
   }
  ],
  "sensors": [
   {
    "id": "xuyi-cneos-schmidt",
    "site": "xuyi-cneos",
    "name": "CNEOS Schmidt telescope, Xuyi",
    "type": "OPTICAL_TELESCOPE",
    "emission": "PASSIVE",
    "apertureM": 1.04,
    "status": "used for GEO debris in 2007; Wikipedia says survey observing since 2006",
    "cite": {
     "url": "https://conference.sdo.esoc.esa.int/proceedings/sdc5/paper/3/SDC5-paper3.pdf",
     "text": "SDC5 paper 3: 1.04 m Schmidt, GEO debris observations in 2007 IADC campaign"
    },
    "note": "Paper states a new debris telescope was in design at that time; the NEO survey instrument was the one used. Wavelength as published: visible optical; CCD 4k x 4k, about 2 x 2 deg field."
   },
   {
    "id": "changchun-sdlr",
    "site": "changchun-slr",
    "name": "Changchun Space Debris Laser Ranging System",
    "type": "LASER_RANGEFINDER",
    "emission": "ACTIVE",
    "apertureM": 0.6,
    "status": "debris ranging begun Feb-May 2014 per ILRS 2015 workshop slides; ILRS lists the station among active stations",
    "cite": {
     "url": "https://ilrs.gsfc.nasa.gov/2015_Technical_Workshop/docs/presentations/3_debris_pdf/3.8c_Liang_pres.pdf",
     "text": "ILRS 2015 Technical Workshop slides (Liang): pulse energy 60 mJ at 532 nm, repetition rate 1-500 Hz; debris ranging test Feb-May 2014 (read with pdftotext)"
    },
    "note": "Pulse energy 60 mJ; average power not published, not computed. Wavelength as published: 532 nm green laser (a single published wavelength); limits not published."
   },
   {
    "id": "shanghai-slr-1",
    "site": "shanghai-sheshan-slr",
    "name": "Shanghai laser ranging station",
    "type": "LASER_RANGEFINDER",
    "emission": "ACTIVE",
    "apertureM": 0.6,
    "status": "OPERATING",
    "cite": {
     "url": "https://ilrs.gsfc.nasa.gov/network/stations/active/SHA2_station_info.html",
     "text": "ILRS: 0.6 m receive, 0.21 m transmit"
    },
    "note": "Laser ranging station; no debris role stated on the page. Wavelength as published: laser 1064 nm primary, 532 nm secondary (ILRS)."
   },
   {
    "id": "kunming-slr-1",
    "site": "kunming-slr",
    "name": "Kunming laser ranging station",
    "type": "LASER_RANGEFINDER",
    "emission": "ACTIVE",
    "apertureM": 0.51,
    "status": "OPERATING",
    "cite": {
     "url": "https://ilrs.gsfc.nasa.gov/network/stations/active/KUN2_station_info.html",
     "text": "ILRS: 0.51 m receive and transmit telescopes"
    },
    "note": "Laser ranging station; no debris role stated. Wavelength as published: laser 1064 nm primary, 532 nm secondary (ILRS)."
   },
   {
    "id": "wuhan-slr-1",
    "site": "wuhan-jiufeng-slr",
    "name": "Wuhan laser ranging station",
    "type": "LASER_RANGEFINDER",
    "emission": "ACTIVE",
    "apertureM": 1,
    "status": "OPERATING",
    "cite": {
     "url": "https://ilrs.gsfc.nasa.gov/network/stations/active/JFNL_station_info.html",
     "text": "ILRS: 1 m receive, 0.2 m transmit"
    },
    "note": "Laser ranging station; no debris role stated. Wavelength as published: laser 1064 nm primary, 532 nm secondary (ILRS)."
   },
   {
    "id": "beijing-slr-1",
    "site": "beijing-slr",
    "name": "Beijing laser ranging station",
    "type": "LASER_RANGEFINDER",
    "emission": "ACTIVE",
    "apertureM": 0.6,
    "status": "OPERATING",
    "cite": {
     "url": "https://ilrs.gsfc.nasa.gov/network/stations/active/BEIL_station_info.html",
     "text": "ILRS: 0.60 m receive, 0.16 m transmit"
    },
    "note": "Laser ranging station; no debris role stated. Wavelength as published: laser 1064 nm primary, 532 nm secondary (ILRS)."
   },
   {
    "id": "xian-ntsc-slr-1",
    "site": "xian-ntsc-slr",
    "name": "Xi'an NTSC laser ranging station",
    "type": "LASER_RANGEFINDER",
    "emission": "ACTIVE",
    "apertureM": 1.05,
    "status": "OPERATING",
    "cite": {
     "url": "https://ilrs.gsfc.nasa.gov/network/stations/active/NTSC_station_info.html",
     "text": "ILRS: 1.05 m receive, 0.2 m transmit"
    },
    "note": "Laser ranging station; no debris role stated. Wavelength as published: laser 1064 nm primary, 532 nm secondary (ILRS)."
   }
  ],
  "inorbit": [],
  "leftOut": [
   [
    "Shanghai Sheshan SLR (ILRS SHA2: 31.0961 N, 121.1866 E, 0.6 m, active)",
    "ILRS values verified, but the debris-ranging role appeared only in a search-result abstract (ADS page returned HTTP 405); no opened page states it."
   ],
   [
    "Kunming / Yunnan Observatory SLR (ILRS KUN2: 25.0298 N, 102.7977 E, 0.51 m, active)",
    "ILRS values verified; debris-ranging role came only from a search-result snippet of an MDPI paper (HTTP 403 on open)."
   ],
   [
    "Beijing SLR (BEIL) and Wuhan SLR (JFNL, 1 m)",
    "ILRS stations are active, but no source opened states a space debris role."
   ],
   [
    "Yao'an station (West Lake Peak, Yao'an, Yunnan; CHES survey since 2018; four 6 m domes finished 2009)",
    "Location in words only from search results; no coordinates on an opened page (PMO pages gave a certificate error)."
   ],
   [
    "Honghe station, Yunnan (PMO, space object and debris observation)",
    "Described only in a search snippet; no coordinates. Note Wikipedia lists a Honghe branch under Jiamusi, which conflicts."
   ],
   [
    "Delingha (Qinghai) PMO branch",
    "Sources tie it to a millimeter-wave telescope, not debris; no coordinates."
   ],
   [
    "PMO Nanjing headquarters 32.064946 N, 118.829677 E",
    "That is the headquarters tag, not a sensor; not used for any station."
   ],
   [
    "Yaoan, Honghe, Delingha (PMO), Lijiang telescopes",
    "Pages 404 or no coordinates."
   ],
   [
    "APOSOS nodes",
    "Source PDFs returned 404; no node places seen on an opened page."
   ]
  ]
 },
 {
  "slug": "china-ttc",
  "name": "China Xi'an Satellite Control Center tracking network (CLTC, PLASSF)",
  "operator": "China Satellite Launch and Tracking Control General (CLTC), PLA Aerospace Force",
  "kind": "TT&C dishes (not radars)",
  "status": "no sites placed",
  "terms": TERMS_WP,
  "credit": "Wikipedia contributors (CC BY-SA 4.0) and the pages cited per row",
  "sites": [
   {
    "slug": "kashgar-ttc",
    "name": "Kashgar TT&C and deep space station",
    "abbr": "Kashgar TT&C and deep.",
    "type": "SATELLITE_GROUND_STATION",
    "kind": "space tracking site",
    "country": "156",
    "lat": 39.468056,
    "lon": 75.993889,
    "precision": "Coordinate of the city of Kashgar as printed on its Wikipedia page (39 deg 28 min 05 sec N, 75 deg 59 min 38 sec E, attributed there to the municipal government);",
    "where": "Kashgar, Xinjiang",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Kashgar",
     "text": "Wikipedia Kashgar: city coordinates. Station named on Wikipedia Chinese Deep Space Network and Xi'an Satellite Control Center pages"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Xi'an Satellite Control Center network (moved to PLASSF after 2016 per Wikipedia)"
     }
    ],
    "status": "UNVERIFIED",
    "statusDate": "2020",
    "note": "Chinese Deep Space Network page: 18 m antenna by 2007, new 35 m antenna 2012, upgraded to four 35 m antenna array in late 2020. No page states current operating status."
   },
   {
    "slug": "jiamusi-ttc",
    "name": "Jiamusi TT&C and deep space station",
    "abbr": "Jiamusi TT&C and deep.",
    "type": "SATELLITE_GROUND_STATION",
    "kind": "space tracking site",
    "country": "156",
    "lat": 46.8,
    "lon": 130.318889,
    "precision": "Coordinate of the city of Jiamusi as printed on its Wikipedia page (46 deg 48 min 00 sec N, 130 deg 19 min 08 sec E, attributed there to the municipal government);",
    "where": "Jiamusi, Heilongjiang",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Jiamusi",
     "text": "Wikipedia Jiamusi: city coordinates. Station facts from Wikipedia Chinese Deep Space Network page"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Xi'an Satellite Control Center network"
     }
    ],
    "status": "UNVERIFIED",
    "statusDate": "2012",
    "note": "Chinese Deep Space Network page lists a 64 m antenna in 2012 upgrades; Xi'an page says in use 2007. Current status not stated."
   },
   {
    "slug": "neuquen-espacio-lejano",
    "name": "Espacio Lejano Station, Neuquen, Argentina",
    "abbr": "Espacio Lejano Station,.",
    "type": "SATELLITE_GROUND_STATION",
    "kind": "space tracking site",
    "country": "032",
    "lat": -38.191389,
    "lon": -70.149444,
    "precision": "Published as 38 deg 11 min 29 sec S, 70 deg 08 min 58 sec W for the station on Wikipedia.",
    "where": "Loncopue Department, Neuquen Province, Argentina",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Espacio_Lejano_Station",
     "text": "Wikipedia Espacio Lejano Station: coordinates, 35 m antenna, operational since 2018"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "China National Space Administration (with CONAE per source)"
     }
    ],
    "status": "OPERATING",
    "statusDate": "2018",
    "note": "Page says operational since 2018 (the Deep Space Network page says inaugurated October 2017)."
   },
   {
    "slug": "swakopmund-ttc",
    "name": "Swakopmund tracking station, Namibia",
    "abbr": "Swakopmund tracking.",
    "type": "SATELLITE_GROUND_STATION",
    "kind": "space tracking site",
    "country": "516",
    "lat": -22.574746,
    "lon": 14.54778,
    "precision": "Published as 22 deg 34 min 29 sec S, 14 deg 32 min 52 sec E on Wikipedia for the station.",
    "where": "Swakopmund, Namibia",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Swakopmund_tracking_station",
     "text": "Wikipedia: coordinates, agreement Oct 2000, opened July 2001, 5 m and 9 m antennas, S band"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Part of China Manned Space Engineering tracking system (per Wikipedia)"
     }
    ],
    "status": "UNVERIFIED",
    "statusDate": "2012",
    "note": "Latest dated event on the page is June 2012 (Shenzhou 9 descent). No end date published."
   },
   {
    "slug": "sanya-ttc",
    "name": "Sanya TT&C station",
    "abbr": "Sanya TT&C station",
    "type": "SATELLITE_GROUND_STATION",
    "kind": "space tracking site",
    "country": "156",
    "lat": 18.253333,
    "lon": 109.503611,
    "precision": "Coordinate of the city of Sanya as printed on its Wikipedia page (18 deg 15 min 12 sec N, 109 deg 30 min 13 sec E, attributed there to the municipal government); station named Sanya on the Xi'an page, exact place not published.",
    "where": "Sanya, Hainan",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Sanya",
     "text": "Wikipedia Sanya: city coordinates. Station listed on the Xi'an Satellite Control Center page as put into use 2008"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Xi'an Satellite Control Center network"
     }
    ],
    "status": "UNVERIFIED",
    "statusDate": "2008",
    "note": "Current status not stated."
   },
   {
    "slug": "weinan-ttc",
    "name": "Weinan TT&C station",
    "abbr": "Weinan TT&C station",
    "type": "SATELLITE_GROUND_STATION",
    "kind": "space tracking site",
    "country": "156",
    "lat": 34.5206,
    "lon": 109.471,
    "precision": "Coordinate printed on the Wikipedia Weinan page (34.5206 N, 109.4710 E), labeled there as the municipal government (Linwei District seat); station named Weinan on the Xi'an page, exact place not published.",
    "where": "Weinan, Shaanxi",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Weinan",
     "text": "Wikipedia Weinan: municipal government coordinates. Station named on the Xi'an Satellite Control Center page, no date"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Xi'an Satellite Control Center network"
     }
    ],
    "status": "UNVERIFIED",
    "note": "No date or status published on the opened pages."
   }
  ],
  "sensors": [
   {
    "id": "kashgar-ttc-antennas",
    "site": "kashgar-ttc",
    "name": "Kashgar TT&C and deep space antennas",
    "type": "TT_AND_C_ANTENNA",
    "emission": "ACTIVE",
    "apertureM": 35,
    "status": "UNVERIFIED",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Chinese_Deep_Space_Network",
     "text": "Wikipedia: 35 m antenna 2012, four 35 m array late 2020"
    },
    "note": "Deep space tracking per source; no current-status statement and no space-debris role stated."
   },
   {
    "id": "jiamusi-ttc-antenna",
    "site": "jiamusi-ttc",
    "name": "Jiamusi deep space antenna",
    "type": "TT_AND_C_ANTENNA",
    "emission": "ACTIVE",
    "apertureM": 64,
    "status": "UNVERIFIED",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Chinese_Deep_Space_Network",
     "text": "Wikipedia: 64 m antenna, 2012"
    },
    "note": "Deep space ground station per source."
   },
   {
    "id": "neuquen-ttc-antenna",
    "site": "neuquen-espacio-lejano",
    "name": "Espacio Lejano 35 m antenna",
    "type": "TT_AND_C_ANTENNA",
    "emission": "ACTIVE",
    "apertureM": 35,
    "status": "OPERATING",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Espacio_Lejano_Station",
     "text": "Wikipedia: 35 m antenna, operational since 2018"
    },
    "note": "No space-object observing role stated on the page."
   },
   {
    "id": "swakopmund-ttc-antennas",
    "site": "swakopmund-ttc",
    "name": "Swakopmund 5 m and 9 m antennas",
    "type": "TT_AND_C_ANTENNA",
    "emission": "ACTIVE",
    "apertureM": 9,
    "status": "UNVERIFIED",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Swakopmund_tracking_station",
     "text": "Wikipedia: two antennas 5 m and 9 m, S band"
    },
    "note": "Tracks Chinese crewed spacecraft re-entry per source."
   },
   {
    "id": "sanya-ttc-antenna",
    "site": "sanya-ttc",
    "name": "Sanya TT&C station antenna",
    "type": "TT_AND_C_ANTENNA",
    "emission": "ACTIVE",
    "status": "UNVERIFIED",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Xi%27an_Satellite_Control_Center",
     "text": "Wikipedia: Sanya station put into use 2008"
    },
    "note": "No antenna details published."
   },
   {
    "id": "weinan-ttc-antenna",
    "site": "weinan-ttc",
    "name": "Weinan TT&C station antenna",
    "type": "TT_AND_C_ANTENNA",
    "emission": "ACTIVE",
    "status": "UNVERIFIED",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Xi%27an_Satellite_Control_Center",
     "text": "Wikipedia: Weinan named as a station"
    },
    "note": "No antenna details published."
   }
  ],
  "inorbit": [],
  "leftOut": [
   [
    "Espacio Lejano Station, Neuquen (Wikipedia tag 38.1914 S, 70.1495 W; 35 m antenna, operating since 2018 per Wikipedia and CSIS)",
    "Coordinates and 35 m size verified, but it is a TT&C and deep space dish; no opened page says it observes space objects as a surveillance sensor, and no allowed sensorType fits. Removed with its site."
   ],
   [
    "Swakopmund TT&C station, Namibia (22.574746 S, 14.54778 E on Wikipedia; 5 m and 9 m S-band dishes; opened July 2001)",
    "Coordinates exist but no opened page states current status (latest documented mission 2012); an AidData snippet claims the agreement expired in 2015."
   ],
   [
    "Kashgar, Jiamusi, Sanya, Changchun, Qingdao, Xiamen, Weinan, Nanning, Lingshui, Menghai, Zhanyi, Western Fujian, Western Hunan domestic stations",
    "Wikipedia Xi'an Satellite Control Center page lists names and towns only (Kashgar 1970, Jiamusi 2007, Sanya 2008, Changchun 1968, Lingshui April 2008); no facility coordinates published on opened pages."
   ],
   [
    "Karachi, Malindi, El Sombrero, Amachuma overseas stations",
    "Named only on Wikipedia list; no coordinates, dates or status; Malindi and Karachi not confirmed by any opened page."
   ],
   [
    "Santiago (Chile), Kiruna (Sweden), Dongara (Australia) CLTC use of SSC stations",
    "CSIS: SSC chose not to renew China's contract in 2020 (Santiago text inconsistent); no coordinates on that page and exact end dates not given."
   ],
   [
    "Rio Gallegos (Argentina), planned Emposat/Ascentio station",
    "CSIS says planned, location not identified, and it is a commercial venture, not a verified site."
   ],
   [
    "Miyun 50 m and Yunnan 40 m deep space antennas",
    "Wikipedia gives only approximate longitude (about 116 E, about 101 E), no latitude."
   ],
   [
    "Xi'an Satellite Control Center itself",
    "A control center, not a sensor."
   ],
   [
    "Qingdao, Xiamen, Nanning, Minxi, Lingshui, Changchun TT&C, Menghai, Zhanyi",
    "Named on the Xi'an page without dates or status; city coordinates not opened."
   ],
   [
    "Karachi, Malindi, Santiago, Kiruna, Dongara, Kiribati",
    "No coordinates, dates or end dates on any opened page."
   ],
   [
    "Yuanwang ships",
    "Mobile, excluded."
   ]
  ]
 },
 {
  "slug": "iran",
  "name": "Iran space monitoring",
  "operator": "Iranian Space Agency / Iranian Armed Forces",
  "kind": "national program",
  "status": "claimed; no independently verified sensor with published coordinates",
  "terms": TERMS_WP,
  "credit": "Wikipedia contributors (CC BY-SA 4.0) and the pages cited per row",
  "sites": [
   {
    "slug": "iran-delijan-space-monitoring",
    "name": "Imam Jafar Sadeq Space Monitoring Center (near Delijan)",
    "abbr": "Imam Jafar Sadeq Space.",
    "type": "OBSERVATION_STATION",
    "kind": "space tracking site",
    "country": "364",
    "lat": 33.99028,
    "lon": 50.68361,
    "precision": "the town of Delijan, Markazi province; the center is reported near it, exact place not published",
    "where": "near Delijan, Markazi province",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Delijan",
     "text": "Wikipedia Delijan: 33 59 25 N 50 41 01 E (town; page does not mention the center)."
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Iranian Space Agency / Ministry of Defense (as reported)"
     }
    ],
    "status": "UNVERIFIED",
    "statusDate": "2013-06",
    "note": "Inaugurated June 2013 per Wikipedia Iranian Space Agency article; capabilities are claimed."
   },
   {
    "slug": "iran-semnan-imam-khomeini",
    "name": "Imam Khomeini Spaceport, Semnan",
    "abbr": "Imam Khomeini.",
    "type": "LAUNCH_SITE",
    "kind": "space tracking site",
    "country": "364",
    "lat": 35.238,
    "lon": 53.950778,
    "precision": "the launch complex itself (35 14 16.8 N 53 57 02.8 E as printed on Wikipedia)",
    "where": "southeast of Semnan city, Semnan province",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Imam_Khomeini_Spaceport",
     "text": "Wikipedia Imam Khomeini Spaceport: 35 14 16.8 N 53 57 02.8 E; airstrikes destroyed it 24 and 26 March 2026 (IISS imagery analysis cited, Oct 2026)"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Iranian Space Agency"
     }
    ],
    "status": "UNVERIFIED",
    "note": "Wikipedia says airstrikes destroyed it in March 2026. The Iranian Space Agency page says the 2008 Semnan launch center had a tracking station and prints 35 14 02 N 53 55 16 E for that."
   },
   {
    "slug": "iran-shahroud-space-center",
    "name": "Shahroud Space Center",
    "abbr": "Shahroud Space Center",
    "type": "LAUNCH_SITE",
    "kind": "space tracking site",
    "country": "364",
    "lat": 36.20083,
    "lon": 55.33389,
    "precision": "the launch site as printed on Wikipedia (36 12 03 N 55 20 02 E), southeast of Shahroud",
    "where": "southeast of Shahroud, Semnan province",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Shahroud_Space_Center",
     "text": "Wikipedia infobox coordinates; IRGC Aerospace Force launch site; reported struck 2024, 2025 and March 2026"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "IRGC Aerospace Force"
     }
    ],
    "status": "UNVERIFIED",
    "note": "Launch site; no tracking sensor described on the page."
   },
   {
    "slug": "iran-mahdasht-alborz",
    "name": "Mahdasht satellite image receiving stations",
    "abbr": "Mahdasht satellite.",
    "type": "SATELLITE_GROUND_STATION",
    "kind": "space tracking site",
    "country": "364",
    "lat": 35.72778,
    "lon": 50.80889,
    "precision": "the city of Mahdasht, Karaj County; the stations are reported in it, exact place not published",
    "where": "Mahdasht, Karaj County, Alborz province",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Mahdasht",
     "text": "Wikipedia Mahdasht: 35 43 40 N 50 48 32 E (city, Karaj County, Alborz). Iranian Space Agency page: image receiving stations installed at Mahdasht and Karaj in 1976 (no coordinates, no place for."
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Iranian Space Agency"
     }
    ],
    "status": "UNVERIFIED",
    "note": "1976 satellite image receiving stations per Iranian Space Agency page. The Alborz Space Center is not placed by any opened page, so it is not claimed here."
   },
   {
    "slug": "iran-chabahar-space-center",
    "name": "Chabahar Space Center",
    "abbr": "Chabahar Space Center",
    "type": "LAUNCH_SITE",
    "kind": "space tracking site",
    "country": "364",
    "lat": 25.29278,
    "lon": 60.64972,
    "precision": "the city of Chabahar; the center is on the coast near it, exact place not published",
    "where": "Chabahar, Sistan and Baluchestan",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/National_Satellite_Launch_Base",
     "text": "Wikipedia National Satellite Launch Base: base is at Chabahar, no coordinates; phase 1 announced complete Feb 2026, phase 2 foreign satellite launches and phase 3 solid-fuel rockets planned, no."
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Iranian Space Agency"
     }
    ],
    "status": "UNDER_CONSTRUCTION",
    "note": "Phase 1 reported complete; later phases pending. No tracking facility described on pages opened."
   },
   {
    "slug": "iran-salmas-ground-station",
    "name": "Salmas satellite ground station",
    "abbr": "Salmas satellite ground.",
    "type": "SATELLITE_GROUND_STATION",
    "kind": "space tracking site",
    "country": "364",
    "lat": 38.20278,
    "lon": 44.76694,
    "precision": "the city of Salmas; station place within it not published",
    "where": "Salmas, West Azerbaijan province",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Salmas",
     "text": "Wikipedia Salmas: 38 12 10 N 44 46 01 E (page does not mention a station). Tasnim 2026-02-05."
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Iranian Space Agency"
     }
    ],
    "status": "OPERATING",
    "statusDate": "2026-02-05",
    "note": "Operating per Iranian state media (Tasnim); the place of the station within Salmas is not published."
   },
   {
    "slug": "iran-chenaran-ground-station",
    "name": "Chenaran satellite ground station",
    "abbr": "Chenaran satellite.",
    "type": "SATELLITE_GROUND_STATION",
    "kind": "space tracking site",
    "country": "364",
    "lat": 36.64056,
    "lon": 59.11861,
    "precision": "the city of Chenaran; station place not published",
    "where": "Chenaran, Razavi Khorasan province",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Chenaran",
     "text": "Wikipedia Chenaran: 36 38 26 N 59 07 07 E (page does not mention a station). Tasnim 2026-02-05: Chenaran to be officially opened in the very near future"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Iranian Space Agency"
     }
    ],
    "status": "PLANNED",
    "statusDate": "2026-02-05",
    "note": "Opening not confirmed on any page opened."
   }
  ],
  "sensors": [
   {
    "id": "iran-delijan-radar",
    "site": "iran-delijan-space-monitoring",
    "name": "Delijan monitoring center radar (claimed)",
    "type": "RADAR_PULSE",
    "emission": "ACTIVE",
    "status": "UNVERIFIED",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Iranian_Space_Agency",
     "text": "Wikipedia: center tracks and detects satellites and space objects using radar, electro-optic and radio systems"
    },
    "note": "Radar type and band not published; sensorType is the nearest enum value, not a stated waveform."
   },
   {
    "id": "iran-delijan-eo",
    "site": "iran-delijan-space-monitoring",
    "name": "Delijan monitoring center electro-optical system (claimed)",
    "type": "OPTICAL_TELESCOPE",
    "emission": "PASSIVE",
    "status": "UNVERIFIED",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Iranian_Space_Agency",
     "text": "Wikipedia: electro-optic system named for the center"
    },
    "note": "Aperture not published."
   },
   {
    "id": "iran-semnan-tracking",
    "site": "iran-semnan-imam-khomeini",
    "name": "Semnan launch center tracking station",
    "type": "TT_AND_C_ANTENNA",
    "emission": "ACTIVE",
    "status": "UNVERIFIED",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Iranian_Space_Agency",
     "text": "Wikipedia: Semnan launch center unveiled 4 Feb 2008 with underground command and control center, tracking station and launchpad"
    },
    "note": "Per Iranian Space Agency page the 2008 Semnan launch center included a tracking station; its own place is not published."
   },
   {
    "id": "iran-salmas-ttc",
    "site": "iran-salmas-ground-station",
    "name": "Salmas satellite control and data reception antennas",
    "type": "TT_AND_C_ANTENNA",
    "emission": "ACTIVE",
    "status": "OPERATING",
    "cite": {
     "url": "https://www.tasnimnews.ir/en/news/2026/02/05/3509551/iran-sharpens-satellite-control-with-new-ground-stations-space-chief",
     "text": "Tasnim: stations host multiple antennas for different frequency bands and wavelengths; page seen does not name bands or confirm command uplink"
    },
    "note": "Per Iranian state media; antenna role as TT&C is the nearest enum value."
   },
   {
    "id": "iran-chenaran-ttc",
    "site": "iran-chenaran-ground-station",
    "name": "Chenaran satellite control and data reception antennas",
    "type": "TT_AND_C_ANTENNA",
    "emission": "ACTIVE",
    "status": "PLANNED",
    "cite": {
     "url": "https://www.tasnimnews.ir/en/news/2026/02/05/3509551/iran-sharpens-satellite-control-with-new-ground-stations-space-chief",
     "text": "Tasnim: Chenaran to be officially opened in the very near future; antennas described as multi-band"
    },
    "note": "Per Iranian state media."
   }
  ],
  "inorbit": [],
  "leftOut": [
   [
    "Imam Jafar Sadeq space monitoring center near Delijan (radar, electro-optic, radio; opened 2013-06-09 per press)",
    "No source opened gives coordinates for the facility; only 'about 200 km south of Tehran' / the town. Sources: https://en.wikipedia.org/wiki/Iranian_Space_Agency (no coordinates), press results. Capabilities are Iranian official claims."
   ],
   [
    "Optical telescope at Mahdasht Space Centre / Alborz Space Center near Karaj (reported 2016 as node of the APSCO optical network)",
    "No coordinates published on any page opened (Alborz Space Center Wikipedia URL returned 404); status unconfirmed; reported by Space Watch ME 2016 (https://spacewatchme.com/2016/09/iran-apsco-create-space-situational-awareness-network/ seen in search results only)."
   ],
   [
    "Sepehr / Ghadir radar family (Wikipedia: seven Ghadir radars known as of 2023; claimed LEO satellite detection)",
    "Wikipedia articles https://en.wikipedia.org/wiki/Sepehr_(radar) and https://en.wikipedia.org/wiki/Phased_array_radar_Ghadir carry no coordinates and no band; sites unspecified; Israel reportedly struck at least six in June 2025 so status unknown;"
   ],
   [
    "Tracking station inside Iranian Space Agency Satellite Launch Center Semnan (Wikipedia tag 35 14 02 N 53 55 16 E is the launch center)",
    "Tag is for the launch center, tracking station has no own coordinates, no sensor specifications."
   ],
   [
    "Sepehr and Ghadir radars",
    "Wikipedia Sepehr (radar) opened: no sites, no coordinates; reported range 3,000 km (Mashregh 2013) vs first phase 300-700 km (Farsnews 2013), both claims; no destruction report on that page. Search results gave only unrelated IRGC claims about US radars."
   ],
   [
    "Qeshm ground station",
    "Only a 2014 forum snippet in search results; Qeshm Wikipedia page opened and does not mention a station."
   ],
   [
    "APSCO telescope at Mahdasht",
    "No opened page states it."
   ],
   [
    "sensor iran-delijan-radio",
    "Source says only \"radio systems\"; passive receive is not stated, so PASSIVE_RF_ESM is unsupported."
   ],
   [
    "sensor iran-mahdasht-tracking",
    "Source (Iranian Space Agency page) describes 1976 satellite image receiving stations, not telemetry/command; ACTIVE TT&C unsupported."
   ]
  ]
 },
 {
  "slug": "north-korea",
  "name": "North Korea satellite control",
  "operator": "National Aerospace Development Administration (DPRK)",
  "kind": "national program",
  "status": "no documented space surveillance sensor",
  "terms": TERMS_WP,
  "credit": "Wikipedia contributors (CC BY-SA 4.0) and the pages cited per row",
  "sites": [
   {
    "slug": "dprk-general-satellite-control-center",
    "name": "General Satellite Control Center, Pyongyang",
    "abbr": "General Satellite.",
    "type": "SATELLITE_GROUND_STATION",
    "kind": "space tracking site",
    "country": "408",
    "lat": 39.0425,
    "lon": 125.7098,
    "precision": "building coordinates as printed on Wikipedia (39 02 33 N 125 42 35 E)",
    "where": "Pothonggang District, Pyongyang",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/General_Satellite_Control_Center",
     "text": "Wikipedia infobox coordinates; main mission control center, built 2000-2005 per page"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Korean Committee of Space Technology (NADA)"
     }
    ],
    "status": "UNVERIFIED",
    "note": "Mission control; page states no current status. Present-tense description only."
   },
   {
    "slug": "dprk-sohae",
    "name": "Sohae Satellite Launching Station",
    "abbr": "Sohae Satellite.",
    "type": "LAUNCH_SITE",
    "kind": "space tracking site",
    "country": "408",
    "lat": 39.66,
    "lon": 124.705,
    "precision": "station center as printed on Wikipedia (39 39 36 N 124 42 18 E)",
    "where": "Tongchang-ri, Cholsan County, North Pyongan",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Sohae_Satellite_Launching_Station",
     "text": "Wikipedia infobox; status Active"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "NADA"
     }
    ],
    "status": "OPERATING",
    "note": "Page does not mention tracking or telemetry facilities."
   },
   {
    "slug": "dprk-tonghae",
    "name": "Tonghae Satellite Launching Ground (Musudan-ri)",
    "abbr": "Tonghae Satellite.",
    "type": "LAUNCH_SITE",
    "kind": "space tracking site",
    "country": "408",
    "lat": 40.85833,
    "lon": 129.68639,
    "precision": "site coordinates as printed on Wikipedia (40 51 30 N 129 41 11 E); the tracking facility has no coordinate of its own",
    "where": "Hwadae County, North Hamgyong",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Tonghae_Satellite_Launching_Ground",
     "text": "Wikipedia: infobox 40 51 30 N 129 41 11 E; caretaker status as of 2020; tracking facilities added in early 1990s; Hwadae County"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "NADA"
     }
    ],
    "status": "UNVERIFIED",
    "note": "Wikipedia says the page coordinates of the Ground Tracking Facility were obtained from Wikimapia and prints none; this row uses the site coordinate only."
   }
  ],
  "sensors": [
   {
    "id": "dprk-tonghae-tracking",
    "site": "dprk-tonghae",
    "name": "Tonghae ground tracking facility",
    "type": "TT_AND_C_ANTENNA",
    "emission": "ACTIVE",
    "status": "UNVERIFIED",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Tonghae_Satellite_Launching_Ground",
     "text": "Wikipedia says tracking facilities were added in early 1990s"
    },
    "note": "Launch range tracking, not space surveillance."
   }
  ],
  "inorbit": [],
  "leftOut": [
   [
    "Pyongyang General Satellite Control Center; telemetry and tracking trailers at Sohae",
    "Launch telemetry and tracking, not documented space surveillance; no coordinates for the control center on any page (38 North results); Sohae launch site 39.660 N 124.705 E from GlobalSecurity (search result only) is the launch site, not a sensor."
   ],
   [
    "Sohae tracking and telemetry",
    "Sohae Wikipedia page opened and lists no tracking facilities."
   ],
   [
    "38 North General Satellite Control Building page",
    "403, not opened."
   ]
  ]
 },
 {
  "slug": "pakistan",
  "name": "Pakistan SUPARCO",
  "operator": "SUPARCO",
  "kind": "national program",
  "status": "no documented space surveillance sensor with coordinates",
  "terms": TERMS_WP,
  "credit": "Wikipedia contributors (CC BY-SA 4.0) and the pages cited per row",
  "sites": [
   {
    "slug": "pk-suparco-islamabad",
    "name": "SUPARCO Satellite Ground Station, Islamabad",
    "abbr": "SUPARCO Satellite.",
    "type": "SATELLITE_GROUND_STATION",
    "kind": "space tracking site",
    "country": "586",
    "lat": 33.69306,
    "lon": 73.06389,
    "precision": "the city of Islamabad; station place not published",
    "where": "Islamabad",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Islamabad",
     "text": "Wikipedia Islamabad: 33 41 35 N 73 03 50 E; SUPARCO article: SGS acquires imagery from Pakistani and foreign satellites"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "SUPARCO"
     }
    ],
    "status": "UNVERIFIED",
    "note": "Ground station per SUPARCO Wikipedia article (https://en.wikipedia.org/wiki/Space_and_Upper_Atmosphere_Research_Commission);"
   },
   {
    "slug": "pk-suparco-karachi",
    "name": "SUPARCO Satellite Ground Station and PAMCC, Karachi",
    "abbr": "SUPARCO Satellite.",
    "type": "SATELLITE_GROUND_STATION",
    "kind": "space tracking site",
    "country": "586",
    "lat": 24.86,
    "lon": 67.01,
    "precision": "the city of Karachi; station place not published",
    "where": "Karachi",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Karachi",
     "text": "Wikipedia Karachi: 24 51 36 N 67 00 36 E; SUPARCO article lists SGS Karachi, PAMCC (Cospas-Sarsat ground segment), navigation ground station 1973"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "SUPARCO"
     }
    ],
    "status": "UNVERIFIED",
    "note": "Ground station per SUPARCO Wikipedia article (https://en.wikipedia.org/wiki/Space_and_Upper_Atmosphere_Research_Commission);"
   },
   {
    "slug": "pk-suparco-lahore",
    "name": "SUPARCO ground station, Lahore area",
    "abbr": "SUPARCO ground station,.",
    "type": "SATELLITE_GROUND_STATION",
    "kind": "space tracking site",
    "country": "586",
    "lat": 31.54972,
    "lon": 74.34361,
    "precision": "the city of Lahore; the station is described as near Lahore, place not published",
    "where": "near Lahore",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Lahore",
     "text": "Wikipedia Lahore: 31 32 59 N 74 20 37 E; SUPARCO article: Landsat ground station established near Lahore 1973, ground station expanded 1983"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "SUPARCO"
     }
    ],
    "status": "UNVERIFIED",
    "note": "Ground station per SUPARCO Wikipedia article (https://en.wikipedia.org/wiki/Space_and_Upper_Atmosphere_Research_Commission);"
   }
  ],
  "sensors": [],
  "inorbit": [],
  "leftOut": [
   [
    "SUPARCO ground stations (Islamabad, Karachi, Lahore), PAMCC Karachi, Sonmiani range",
    "https://en.wikipedia.org/wiki/Space_and_Upper_Atmosphere_Research_Commission lists them with no coordinates; they are imagery/telemetry/search-and-rescue/launch facilities, not space surveillance."
   ],
   [
    "Pakistani APOSOS optical node (press: Iran, Pakistan, China, Peru monitoring debris through APSCO observatories)",
    "No site name or coordinates found on any opened page (APOSOS PDFs at swfound.org returned 404, NAOC page failed certificate check)."
   ],
   [
    "Sonmiani Flight Test Range",
    "Sounding rocket range, no coordinate page opened."
   ],
   [
    "Lahore/Karachi PakSat-1R TT&C stations (Kala Shah Kaku)",
    "Wikipedia PakSat-1R 404 and GlobalSecurity 403; only search snippets."
   ],
   [
    "sensor pk-islamabad-sgs",
    "Source says the station acquires satellite imagery (receive); no TT&C or tracking stated, ACTIVE TT&C unsupported."
   ],
   [
    "sensor pk-lahore-gs",
    "Source says Landsat ground station near Lahore and 1983 expansion tied to communication satellites; no TT&C or tracking stated."
   ]
  ]
 },
 {
  "slug": "turkey",
  "name": "Turkey space surveillance",
  "operator": "TUBITAK UZAY / Turkish Space Agency / ASELSAN / Ataturk University",
  "kind": "national program",
  "status": "planned and partial",
  "terms": TERMS_WP,
  "credit": "Wikipedia contributors (CC BY-SA 4.0) and the pages cited per row",
  "sites": [
   {
    "slug": "tr-golbasi-ground-station",
    "name": "Golbasi Ground Station (Tuerksat)",
    "abbr": "Golbasi Ground Station.",
    "type": "SATELLITE_GROUND_STATION",
    "kind": "space tracking site",
    "country": "792",
    "lat": 39.79528,
    "lon": 32.80278,
    "precision": "the Golbasi district town; the station is in the district, exact place not published",
    "where": "Golbasi, Ankara",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/G%C3%B6lba%C5%9F%C4%B1,_Ankara",
     "text": "Wikipedia Golbasi: 39 47 43 N 32 48 10 E; lists Golbasi Ground Station of Tuerksat."
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Tuerksat"
     }
    ],
    "status": "UNVERIFIED",
    "note": "Commercial telecom satellite control, not space surveillance."
   }
  ],
  "sensors": [
   {
    "id": "tr-golbasi-antennas",
    "site": "tr-golbasi-ground-station",
    "name": "Golbasi 9 m and 11 m antennas",
    "type": "TT_AND_C_ANTENNA",
    "emission": "ACTIVE",
    "apertureM": 11,
    "status": "UNVERIFIED",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/G%C3%B6lba%C5%9F%C4%B1_Ground_Station",
     "text": "Wikipedia: 9 m and 11 m parabolic antennas at main site (backup has a 9 m); article covers telecommunications links, tracking not mentioned"
    },
    "note": "Telecom satellite control; tracking not stated, TT&C is the nearest enum value; page flagged as needing update (Nov 2024)."
   }
  ],
  "inorbit": [],
  "leftOut": [
   [
    "ASELSAN Special Telescope System (YAKUD near-space SSA project) at Ataturk University DAG campus, Erzurum",
    "https://eski.atauni.edu.tr/en/aselsan-a-ait-ozel-teleskop-sistemi-dag-yerleskesinde-kurulacak (27 Sep 2023) says it is to be installed (planned); no aperture;"
   ],
   [
    "TUBITAK UZAY SSA sensors; APOSOS role",
    "Pages only describe services and working groups; no sensor sites or coordinates."
   ],
   [
    "TUBITAK National Observatory (Bakirlitepe), 36.82417 N 30.33556 E on Wikipedia",
    "Page and TUG T100 search result describe astronomy; no space surveillance role found on any opened page."
   ],
   [
    "Eastern Anatolia Observatory (39.783 N 41.233 E)",
    "Wikipedia opened: 4 m telescope, first light 2024; no SSA mention."
   ]
  ]
 },
 {
  "slug": "saudi-arabia",
  "name": "Saudi space situational awareness",
  "operator": "Saudi Space Agency",
  "kind": "national program",
  "status": "agreements only",
  "terms": TERMS_UNSTATED,
  "credit": "the pages cited per row",
  "sites": [
   {
    "slug": "sa-salro-riyadh",
    "name": "Saudi Laser Ranging Observatory (SALRO), Riyadh",
    "abbr": "Saudi Laser Ranging.",
    "type": "RESEARCH_FACILITY",
    "kind": "space tracking site",
    "country": "682",
    "lat": 24.9102,
    "lon": 46.4004,
    "alt": 773,
    "precision": "station coordinates published on ILRS page",
    "where": "Riyadh",
    "cite": {
     "url": "https://ilrs.gsfc.nasa.gov/network/stations/inactive/RIYL_station_info.html",
     "text": "ILRS RIYL: 24.9102 N 46.4004 E 773 m"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "KACST Space Research Institute"
     }
    ],
    "status": "UNVERIFIED",
    "statusDate": "2002-05-09",
    "note": "Page is under the inactive path; its date is 2002-05-09; operating status not stated."
   }
  ],
  "sensors": [
   {
    "id": "sa-salro-slr",
    "site": "sa-salro-riyadh",
    "name": "SALRO laser ranging system",
    "type": "LASER_RANGEFINDER",
    "emission": "ACTIVE",
    "apertureM": 0.75,
    "status": "UNVERIFIED",
    "cite": {
     "url": "https://ilrs.gsfc.nasa.gov/network/stations/inactive/RIYL_station_info.html",
     "text": "ILRS RIYL: 0.73 m receive, 0.75 m transmit, 1064 nm primary, 532 nm secondary, 12 Hz max"
    },
    "note": "Satellite laser ranging to cooperative targets per ILRS. Wavelength as published: 1064 nm and 532 nm."
   }
  ],
  "inorbit": [],
  "leftOut": [
   [
    "Saudi Space Agency MoUs with LeoLabs and NorthStar (Feb 2024)",
    "Agreements only; no Saudi-owned sensor site found."
   ]
  ]
 },
 {
  "slug": "laser-ranging",
  "name": "Satellite laser ranging stations",
  "operator": "the station operators named in each row (stations of the International Laser Ranging Service)",
  "kind": "laser ranging stations that track spacecraft and, where the row says so, debris",
  "status": "each row carries its own status",
  "terms": TERMS_UNSTATED,
  "credit": "the pages cited per row",
  "sites": [
   {
    "slug": "helwan-slr",
    "name": "Helwan SLR station",
    "abbr": "HLWL",
    "type": "RESEARCH_FACILITY",
    "kind": "space tracking site",
    "country": "818",
    "lat": 29.859,
    "lon": 31.3427,
    "alt": 145.46,
    "precision": "ILRS site-log station coordinates for the SLR system itself (4 decimals)",
    "where": "Helwan, Egypt",
    "cite": {
     "url": "https://ilrs.cddis.eosdis.nasa.gov/network/stations/inactive/HLWL_station_info.html",
     "text": "ILRS HLWL station information page"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "NRIAG"
     }
    ],
    "status": "DECOMMISSIONED",
    "note": "RETIRED. Listed under ILRS inactive stations; the log page itself gives a conflicting campaign-operation note."
   },
   {
    "slug": "hartebeesthoek-slr",
    "name": "Hartebeesthoek SLR stations",
    "abbr": "HartRAO",
    "type": "ASTRONOMICAL_OBSERVATORY",
    "kind": "space tracking site",
    "country": "710",
    "lat": -25.8897,
    "lon": 27.6861,
    "alt": 1406.8,
    "precision": "ILRS HARL station coordinates (4 decimals); the HRTL system lists 25.889194 S 27.6861111 E, same site within about 20 m. Wikipedia tag not re-checked.",
    "where": "Hartebeesthoek, Gauteng, South Africa",
    "cite": {
     "url": "https://ilrs.cddis.eosdis.nasa.gov/network/stations/active/HARL_station_info.html",
     "text": "ILRS HARL station information"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "HartRAO (National Research Foundation)"
     }
    ],
    "status": "OPERATING",
    "note": ""
   },
   {
    "slug": "san-juan-slr",
    "name": "San Juan SLR station",
    "abbr": "SJUL",
    "type": "OBSERVATION_STATION",
    "kind": "space tracking site",
    "country": "032",
    "lat": -31.5086249,
    "lon": -68.6231602,
    "alt": 727.2,
    "precision": "ILRS site-log station coordinates; the page prints longitude as E but the geocentric X/Y/Z values place it west, so the sign was taken from that",
    "where": "San Juan Province, about 12 km from San Juan city, Argentina",
    "cite": {
     "url": "https://ilrs.cddis.eosdis.nasa.gov/network/stations/active/SJUL_station_info.html",
     "text": "ILRS SJUL station information"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Felix Aguilar Astronomical Observatory, National University of San Juan"
     }
    ],
    "status": "OPERATING",
    "note": ""
   },
   {
    "slug": "arequipa-slr",
    "name": "Arequipa SLR station",
    "abbr": "AREL",
    "type": "RESEARCH_FACILITY",
    "kind": "space tracking site",
    "country": "604",
    "lat": -16.4657,
    "lon": -71.493,
    "alt": 2489.05,
    "precision": "ILRS site-log station coordinates (4 decimals)",
    "where": "Characato, Arequipa, Peru",
    "cite": {
     "url": "https://ilrs.cddis.eosdis.nasa.gov/network/stations/active/AREL_station_info.html",
     "text": "ILRS AREL station information"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "UNSA"
     }
    ],
    "status": "OPERATING",
    "note": ""
   },
   {
    "slug": "matera-cgs",
    "name": "Matera Laser Ranging Observatory (ILRS MATM)",
    "abbr": "Matera Laser Ranging.",
    "type": "RESEARCH_FACILITY",
    "kind": "space tracking site",
    "country": "380",
    "lat": 40.6486,
    "lon": 16.7046,
    "alt": 536.9,
    "precision": "ILRS station coordinates",
    "where": "Matera, Italy",
    "cite": {
     "url": "https://ilrs.gsfc.nasa.gov/network/stations/active/MATM_station_info.html",
     "text": "ILRS MATM station info"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "ASI"
     }
    ],
    "status": "OPERATING",
    "note": "Listed under active stations"
   },
   {
    "slug": "graz-lustbuehel",
    "name": "Graz Lustbuehel SLR station (ILRS GRZL)",
    "abbr": "Graz Lustbuehel SLR.",
    "type": "RESEARCH_FACILITY",
    "kind": "space tracking site",
    "country": "040",
    "lat": 47.0671,
    "lon": 15.4933,
    "alt": 539.4,
    "precision": "ILRS station coordinates",
    "where": "Graz, Austria",
    "cite": {
     "url": "https://ilrs.gsfc.nasa.gov/network/stations/active/GRZL_station_info.html",
     "text": "ILRS GRZL station info"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "IWF"
     }
    ],
    "status": "OPERATING",
    "note": "Active station listing"
   },
   {
    "slug": "borowiec",
    "name": "Borowiec SLR station (ILRS BORL)",
    "abbr": "Borowiec SLR station.",
    "type": "RESEARCH_FACILITY",
    "kind": "space tracking site",
    "country": "616",
    "lat": 52.277,
    "lon": 17.0746,
    "alt": 123.4,
    "precision": "ILRS station coordinates",
    "where": "Borowiec near Kornik, Poland",
    "cite": {
     "url": "https://ilrs.gsfc.nasa.gov/network/stations/active/BORL_station_info.html",
     "text": "ILRS BORL station info"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "CBK PAN"
     }
    ],
    "status": "OPERATING",
    "note": "Listed under ILRS active stations"
   },
   {
    "slug": "riga-slr",
    "name": "Riga SLR station (ILRS RIGL)",
    "abbr": "Riga SLR station (ILRS.",
    "type": "RESEARCH_FACILITY",
    "kind": "space tracking site",
    "country": "428",
    "lat": 56.948553,
    "lon": 24.05908,
    "alt": 31.35,
    "precision": "ILRS station coordinates",
    "where": "Riga, Latvia",
    "cite": {
     "url": "https://ilrs.gsfc.nasa.gov/network/stations/active/RIGL_station_info.html",
     "text": "ILRS RIGL station info"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "University of Latvia"
     }
    ],
    "status": "OPERATING",
    "note": "Active-station listing"
   },
   {
    "slug": "herstmonceux-sgf",
    "name": "Herstmonceux SLR station (ILRS HERL)",
    "abbr": "Herstmonceux SLR.",
    "type": "RESEARCH_FACILITY",
    "kind": "space tracking site",
    "country": "826",
    "lat": 50.8674,
    "lon": 0.3361,
    "alt": 75,
    "precision": "ILRS station coordinates for the laser station",
    "where": "Herstmonceux, East Sussex, UK",
    "cite": {
     "url": "https://ilrs.gsfc.nasa.gov/network/stations/active/HERL_station_info.html",
     "text": "ILRS HERL station info"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "NERC"
     }
    ],
    "status": "OPERATING",
    "note": "Active-station listing"
   }
  ],
  "sensors": [
   {
    "id": "egypt-helwan-slr",
    "site": "helwan-slr",
    "name": "Helwan SLR (ILRS HLWL)",
    "type": "LASER_RANGEFINDER",
    "emission": "ACTIVE",
    "apertureM": 0.4,
    "status": "retired",
    "cite": {
     "url": "https://ilrs.cddis.eosdis.nasa.gov/network/stations/inactive/HLWL_station_info.html",
     "text": "ILRS Helwan station info: receive telescope 0.4 m, 5 Hz max, installed 1983-05-01"
    },
    "note": "RETIRED. Transmit telescope 0.38 m. Wavelength as published: Optical; Nd:YAG 532 nm secondary (primary 1064 nm); limits not applicable."
   },
   {
    "id": "za-harl-moblas6",
    "site": "hartebeesthoek-slr",
    "name": "Hartebeesthoek SLR HARL (MOBLAS-6, ILRS 7501)",
    "type": "LASER_RANGEFINDER",
    "emission": "ACTIVE",
    "apertureM": 0.762,
    "status": "OPERATING (ILRS active list; site log prepared 2021-09-27); system installed 2000-06-09",
    "cite": {
     "url": "https://ilrs.cddis.eosdis.nasa.gov/network/stations/active/HARL_station_info.html",
     "text": "ILRS HARL station info"
    },
    "note": "Transmit refractor 0.163 m; NASA subnetwork. Wavelength as published: Optical; Nd:YAG 1064 nm primary, 532.1 nm secondary; 10 Hz max; limits not applicable."
   },
   {
    "id": "za-hrtl-sazhen",
    "site": "hartebeesthoek-slr",
    "name": "Hartebeesthoek SLR HRTL (Sazhen-TM-OWS, ILRS 7503)",
    "type": "LASER_RANGEFINDER",
    "emission": "ACTIVE",
    "apertureM": 0.25,
    "status": "OPERATING (ILRS active list; operating since 2016-12-16)",
    "cite": {
     "url": "https://ilrs.cddis.eosdis.nasa.gov/network/stations/active/HRTL_station_info.html",
     "text": "ILRS HRTL station info"
    },
    "note": "On-site agency SARAO; responsible agency JC RPC PSI Moscow. Wavelength as published: Optical; Nd:YAG 1064 nm primary, 532 nm secondary; 300 Hz max; limits not applicable."
   },
   {
    "id": "ar-sjul-slr",
    "site": "san-juan-slr",
    "name": "San Juan SLR (ILRS 7406, SJUL)",
    "type": "LASER_RANGEFINDER",
    "emission": "ACTIVE",
    "apertureM": 0.6,
    "status": "ILRS active listing, first applicable 2006-02-22; site log last updated 2018, current status unconfirmed",
    "cite": {
     "url": "https://ilrs.cddis.eosdis.nasa.gov/network/stations/active/SJUL_station_info.html",
     "text": "ILRS SJUL station info"
    },
    "note": "Receive telescope 0.60 m; transmit 0.16 m. Page labels longitude E; value is west of Greenwich. Wavelength as published: Optical; Nd:YAG 1064 nm primary, 532 nm secondary; 20 Hz max; limits not applicable."
   },
   {
    "id": "pe-arel-slr",
    "site": "arequipa-slr",
    "name": "Arequipa SLR (ILRS 7403, AREL)",
    "type": "LASER_RANGEFINDER",
    "emission": "ACTIVE",
    "apertureM": 0.28,
    "status": "ILRS active listing; current status unconfirmed",
    "cite": {
     "url": "https://ilrs.cddis.eosdis.nasa.gov/network/stations/active/AREL_station_info.html",
     "text": "ILRS AREL station info"
    },
    "note": "Schmidt-Cassegrain, same aperture for transmit and receive; NASA responsible agency. Wavelength as published: Optical; Nd:YAG 1064 nm primary, 532 nm secondary; 10 Hz max; limits not applicable."
   },
   {
    "id": "mlro",
    "site": "matera-cgs",
    "name": "MLRO 1.5 m",
    "type": "LASER_RANGEFINDER",
    "emission": "ACTIVE",
    "apertureM": 1.5,
    "status": "OPERATING",
    "cite": {
     "url": "https://ilrs.gsfc.nasa.gov/network/stations/active/MATM_station_info.html",
     "text": "1.50 m receive and transmit telescopes"
    },
    "note": "ILRS lists 1.50 m receive and 1.50 m transmit"
   },
   {
    "id": "grzl-debris",
    "site": "graz-lustbuehel",
    "name": "Graz SLR 0.5 m, debris laser",
    "type": "LASER_RANGEFINDER",
    "emission": "ACTIVE",
    "apertureM": 0.5,
    "status": "OPERATING",
    "cite": {
     "url": "https://ilrs.gsfc.nasa.gov/network/stations/active/GRZL_station_info.html",
     "text": "0.5 m receive; 140 mJ 1064 nm laser dedicated to space debris ranging (installed 2018-06-20)"
    },
    "note": "ILRS log: laser entry 5.03 installed 2018-06-20, 1064 nm 140 mJ, dedicated to space debris ranging; 0.5 m receive verified"
   },
   {
    "id": "borl",
    "site": "borowiec",
    "name": "Borowiec SLR 0.65 m",
    "type": "LASER_RANGEFINDER",
    "emission": "ACTIVE",
    "apertureM": 0.65,
    "status": "OPERATING",
    "cite": {
     "url": "https://ilrs.gsfc.nasa.gov/network/stations/active/BORL_station_info.html",
     "text": "0.65 m receiving Cassegrain"
    },
    "note": ""
   },
   {
    "id": "rigl",
    "site": "riga-slr",
    "name": "Riga SLR 1.0 m",
    "type": "LASER_RANGEFINDER",
    "emission": "ACTIVE",
    "apertureM": 1,
    "status": "OPERATING",
    "cite": {
     "url": "https://ilrs.gsfc.nasa.gov/network/stations/active/RIGL_station_info.html",
     "text": "1.0 m receive/transmit"
    },
    "note": "current telescope entry 4.03 installed 2019-02-14, 1.0 m"
   },
   {
    "id": "herl",
    "site": "herstmonceux-sgf",
    "name": "Herstmonceux SLR 0.5 m",
    "type": "LASER_RANGEFINDER",
    "emission": "ACTIVE",
    "apertureM": 0.5,
    "status": "OPERATING",
    "cite": {
     "url": "https://ilrs.gsfc.nasa.gov/network/stations/active/HERL_station_info.html",
     "text": "0.5 m Cassegrain receive"
    },
    "note": ""
   }
  ],
  "inorbit": [],
  "leftOut": [
   [
    "Kottamia Optical Satellite Tracking Station (OSTS), 0.28 m telescope, set up March 2019, part of ISON",
    "Facts were seen only in search-result snippets (ADS abstract, MDPI 4493); pages returned 403/405 or no text. Kottamia observatory Wikipedia tag 29 56 02 N 31 49 40 E does not mention the OSTS."
   ],
   [
    "OWL-Net or other hosted sensors in South Africa",
    "None found on pages opened (OWL-Net stations are Mongolia, Morocco, Israel, US, Korea)."
   ],
   [
    "Argentine-owned space surveillance sensors",
    "None found."
   ],
   [
    "BIRALES, Medicina receiver, Quirra transmitter",
    "Wikipedia Medicina page has coordinates (44 31 15 N, 11 38 49 E) but does not mention BIRALES, Quirra not sourced"
   ],
   [
    "MFDR, PdM-MiTe, Loiano",
    "not researched within budget"
   ]
  ]
 },
 {
  "slug": "brazil",
  "name": "Brazil",
  "operator": "LNA / Brazilian Space Agency / Roscosmos partners",
  "kind": "national program with Russian-supplied sensors",
  "status": "partial",
  "terms": TERMS_WP,
  "credit": "Wikipedia contributors (CC BY-SA 4.0) and the pages cited per row",
  "sites": [
   {
    "slug": "pico-dos-dias",
    "name": "Pico dos Dias Observatory",
    "abbr": "Pico dos Dias.",
    "type": "ASTRONOMICAL_OBSERVATORY",
    "kind": "space tracking site",
    "country": "076",
    "lat": -22.5344,
    "lon": -45.5825,
    "precision": "Wikipedia coordinate tag on the observatory's own article, 22 32 04 S 45 34 57 W written as decimal degrees",
    "where": "Brasopolis, Minas Gerais, Brazil",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Pico_dos_Dias_Observatory",
     "text": "Wikipedia Pico dos Dias Observatory coordinate tag"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "National Astrophysics Laboratory (LNA)"
     }
    ],
    "status": "OPERATING",
    "note": ""
   },
   {
    "slug": "brasilia-slr",
    "name": "Brasilia SLR station",
    "abbr": "Brasilia SLR station",
    "type": "RESEARCH_FACILITY",
    "kind": "space tracking site",
    "country": "076",
    "lat": -15.7731,
    "lon": -47.8653,
    "alt": 1029.24,
    "precision": "ILRS site-log station coordinates (4 decimals)",
    "where": "Brasilia, Brazil",
    "cite": {
     "url": "https://ilrs.cddis.eosdis.nasa.gov/network/stations/active/BRAL_station_info.html",
     "text": "ILRS BRAL station information"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Responsible agency OJC RPC PSI (Russia); on-site agency not published"
     }
    ],
    "status": "OPERATING",
    "note": ""
   }
  ],
  "sensors": [
   {
    "id": "br-aspos-okp-pico",
    "site": "pico-dos-dias",
    "name": "Roscosmos ASPOS OKP station, Pico dos Dias",
    "type": "OPTICAL_TELESCOPE",
    "emission": "PASSIVE",
    "status": "Inaugurated 2017-04-05; current status not confirmed",
    "cite": {
     "url": "https://revistapesquisa.fapesp.br/um-telescopio-para-monitorar-sucata-espacial/",
     "text": "Pesquisa FAPESP: Russian telescope at Pico dos Dias, operated by LNA; Wikipedia Pico dos Dias article names it ASPOS OKP"
    },
    "note": "Aperture not stated; FAPESP says it can detect debris of 12 cm or more. Wikipedia says Roscosmos opened the first ASPOS OKP station there in April 2017; the Wikipedia article does not call it a telescope, FAPESP does. Optical type taken from FAPESP."
   },
   {
    "id": "br-brasilia-sazhen",
    "site": "brasilia-slr",
    "name": "Brasilia SLR (ILRS 7407, BRAL)",
    "type": "LASER_RANGEFINDER",
    "emission": "ACTIVE",
    "apertureM": 0.25,
    "status": "OPERATING (ILRS active list; first applicable 2014-05-13; equipment installed 2014-05-09; log prepared 2014-08-26)",
    "cite": {
     "url": "https://ilrs.cddis.eosdis.nasa.gov/network/stations/active/BRAL_station_info.html",
     "text": "ILRS BRAL station info"
    },
    "note": "Responsible agency OJC RPC PSI Moscow; on-site contact blank. Wavelength as published: Optical; Nd:YAG 1064 nm primary, 532 nm secondary; 300 Hz max; limits not applicable."
   }
  ],
  "inorbit": [],
  "leftOut": [
   [
    "Bearn trajectography radar at Barreira do Inferno Launch Center (used for space object tracking since May 2021 per press)",
    "Seen only in a search snippet (Dialogo Americas page returned 403); Wikipedia Barreira do Inferno article (tag 5 55 30 S 35 09 47 W) does not mention Bearn; no band published."
   ],
   [
    "COPE / COMAE",
    "Operations center, no sensor."
   ]
  ]
 },
 {
  "slug": "kasi-owl-net",
  "name": "KASI OWL-Net",
  "operator": "Korea Astronomy and Space Science Institute",
  "kind": "national program",
  "status": "operating (full operational capability 2018 per KeepTrack; five stations observing in Nov 2022 per KASI IAWN report)",
  "terms": TERMS_WP,
  "credit": "Wikipedia contributors (CC BY-SA 4.0) and the pages cited per row",
  "sites": [
   {
    "slug": "owl-net-songino-mn",
    "name": "OWL-Net Songino Station",
    "abbr": "OWL-Net Songino Station",
    "type": "OBSERVATION_STATION",
    "kind": "space tracking site",
    "country": "496",
    "lat": 47.886111,
    "lon": 106.334722,
    "alt": 1674,
    "precision": "station coordinates published in the 2016 paper in degrees-minutes-seconds (47 53 10 N, 106 20 5 E) written here as decimal degrees; about 50 km west of Ulaanbaatar at the IAG Songino site",
    "where": "Songino site of the Institute of Astrophysics and Geophysics, Mongolia",
    "cite": {
     "url": "https://koreascience.kr/article/JAKO201618241377396.pub",
     "text": "Station No. 1 coordinates 106 20 5 E, 47 53 10 N; altitude 1,674 m; set up May 2014"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Korea Astronomy and Space Science Institute"
     }
    ],
    "status": "OPERATING",
    "statusDate": "2022-11",
    "note": "Status from KASI IAWN 2023 report listing Songino observations on 2022-11-23."
   },
   {
    "slug": "owl-net-oukaimeden-ma",
    "name": "OWL-Net Oukaimeden Station",
    "abbr": "OWL-Net Oukaimeden.",
    "type": "OBSERVATION_STATION",
    "kind": "space tracking site",
    "country": "504",
    "lat": 31.2061,
    "lon": -7.8664,
    "precision": "Wikipedia coordinate tag of Oukaimeden Observatory, which hosts the station; the telescope's own spot is not published",
    "where": "Oukaimeden Observatory, Morocco",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Ouka\u00efmeden_Observatory",
     "text": "Coordinates 31.2061 N, 7.8664 W; infobox lists observatory code Z01 (OWL-Net)"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Korea Astronomy and Space Science Institute"
     }
    ],
    "status": "OPERATING",
    "statusDate": "2022-11",
    "note": "Status from KASI IAWN 2023 report listing Oukaimeden observations on 2022-11-23/24. Checked: Wikipedia lists Z01 (OWL-Net) in infobox only; observatory's own code is J43."
   },
   {
    "slug": "owl-net-mitzpe-ramon-il",
    "name": "OWL-Net Mitzpe Ramon Station",
    "abbr": "OWL-Net Mitzpe Ramon.",
    "type": "OBSERVATION_STATION",
    "kind": "space tracking site",
    "country": "376",
    "lat": 30.597389,
    "lon": 34.762194,
    "precision": "Wikipedia coordinate tag of Wise Observatory (published 30 35 50.6 N, 34 45 43.9 E, also given as decimal), which hosts the OWL-Net node; about 5 km west of the town of Mitzpe Ramon",
    "where": "Wise Observatory, near Mitzpe Ramon, Israel",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Wise_Observatory",
     "text": "Page says its 50 cm wide-field telescope installed 2016 is a node of the Korean OWL-Net; MPC code M33 per IAWN report"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Korea Astronomy and Space Science Institute"
     }
    ],
    "status": "OPERATING",
    "statusDate": "2022-11",
    "note": "Status from KASI IAWN 2023 report."
   },
   {
    "slug": "owl-net-mt-lemmon-us",
    "name": "OWL-Net Mt. Lemmon Station",
    "abbr": "OWL-Net Mt.",
    "type": "OBSERVATION_STATION",
    "kind": "space tracking site",
    "country": "840",
    "lat": 32.442,
    "lon": -110.7893,
    "precision": "Wikipedia coordinate tag of Mount Lemmon Observatory; the OWL-Net telescope is listed by KASI as at Mt. Lemmon, its exact spot on the mountain is not published and the Wikipedia page does not mention OWL-Net;",
    "where": "Mount Lemmon, Arizona",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Mount_Lemmon_Observatory",
     "text": "Coordinates 32.4420 N, 110.7893 W; site name OWL-Net Mt. Lemmon from the KASI IAWN 2023 status report https://IAWN.net/documents/20230207_16th_Vienna/iawn-2023a-status-report-kasi.pdf"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Korea Astronomy and Space Science Institute"
     }
    ],
    "status": "OPERATING",
    "statusDate": "2022-11",
    "note": "Status from KASI IAWN 2023 report."
   }
  ],
  "sensors": [
   {
    "id": "owl-net-songino-0p5m",
    "site": "owl-net-songino-mn",
    "name": "OWL-Net Songino 0.5 m",
    "type": "OPTICAL_TELESCOPE",
    "emission": "PASSIVE",
    "apertureM": 0.5,
    "status": "OPERATING",
    "cite": {
     "url": "https://koreascience.kr/article/JAKO201618241377396.pub",
     "text": "500 mm reflective optics, alt-azimuth mount, CCD"
    },
    "note": ""
   },
   {
    "id": "owl-net-oukaimeden-0p5m",
    "site": "owl-net-oukaimeden-ma",
    "name": "OWL-Net Oukaimeden 0.5 m",
    "type": "OPTICAL_TELESCOPE",
    "emission": "PASSIVE",
    "apertureM": 0.5,
    "status": "OPERATING",
    "cite": {
     "url": "https://www.keeptrack.space/deep-dive/korean-optical-wide-field-patrol-network",
     "text": "Five identical 0.5 m telescopes, 1.1 degree field of view"
    },
    "note": ""
   },
   {
    "id": "owl-net-mitzpe-ramon-0p5m",
    "site": "owl-net-mitzpe-ramon-il",
    "name": "OWL-Net Mitzpe Ramon 0.5 m",
    "type": "OPTICAL_TELESCOPE",
    "emission": "PASSIVE",
    "apertureM": 0.5,
    "status": "OPERATING",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Wise_Observatory",
     "text": "50 cm wide-field telescope installed 2016, node of OWL-Net"
    },
    "note": ""
   },
   {
    "id": "owl-net-mt-lemmon-0p5m",
    "site": "owl-net-mt-lemmon-us",
    "name": "OWL-Net Mt. Lemmon 0.5 m",
    "type": "OPTICAL_TELESCOPE",
    "emission": "PASSIVE",
    "apertureM": 0.5,
    "status": "OPERATING",
    "cite": {
     "url": "https://www.keeptrack.space/deep-dive/korean-optical-wide-field-patrol-network",
     "text": "Five identical 0.5 m telescopes in Mongolia, Morocco, Israel, United States, South Korea"
    },
    "note": ""
   }
  ],
  "inorbit": [],
  "leftOut": [
   [
    "OWL-Net Daejeon (Daedeok) station and Mt. Bohyun testbed",
    "Wikipedia KASI page has no coordinates; no opened source gave station coordinates"
   ]
  ]
 },
 {
  "slug": "jaxa-nyukasa",
  "name": "JAXA Nyukasayama GEO debris optical facility",
  "operator": "Japan Aerospace Exploration Agency (IAT/JAXA)",
  "kind": "national agency program",
  "status": "built 2006; current status unconfirmed",
  "terms": TERMS_UNSTATED,
  "credit": "the pages cited per row",
  "sites": [
   {
    "slug": "jaxa-nyukasayama",
    "name": "JAXA Nyukasayama Observatory",
    "abbr": "JAXA Nyukasayama.",
    "type": "OBSERVATION_STATION",
    "kind": "space tracking site",
    "country": "392",
    "lat": 35.901389,
    "lon": 138.171667,
    "alt": 1870,
    "precision": "published degrees-minutes-seconds (35 54 05 N, 138 10 18 E) written here as decimal degrees; coordinates are for the facility",
    "where": "Nyukasa highlands, Nagano Prefecture",
    "cite": {
     "url": "https://amostech.com/TechnicalPapers/2007/Orbital_Debris/Nakajima.pdf",
     "text": "Location Nyukasa-highlands, Nagano: Lat 35 54 05, Long 138 10 18 E, Alt 1,870 m"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Japan Aerospace Exploration Agency"
     }
    ],
    "status": "OPERATING",
    "statusDate": "2007",
    "note": "Status as of the 2007 AMOS paper only; no later status found. Checked against the AMOS 2007 paper text: coordinates, altitude confirmed; status is the 2007 value only."
   }
  ],
  "sensors": [
   {
    "id": "jaxa-nyukasa-35cm",
    "site": "jaxa-nyukasayama",
    "name": "Nyukasayama 35 cm Takahashi epsilon-350",
    "type": "OPTICAL_TELESCOPE",
    "emission": "PASSIVE",
    "apertureM": 0.355,
    "focalM": 1.248,
    "status": "OPERATING (as of 2007)",
    "cite": {
     "url": "https://amostech.com/TechnicalPapers/2007/Orbital_Debris/Nakajima.pdf",
     "text": "Table 1: phi 355 mm, f 1,248 mm, F/3.6"
    },
    "note": ""
   },
   {
    "id": "jaxa-nyukasa-25cm",
    "site": "jaxa-nyukasayama",
    "name": "Nyukasayama 25 cm Takahashi BRC-250M",
    "type": "OPTICAL_TELESCOPE",
    "emission": "PASSIVE",
    "apertureM": 0.25,
    "focalM": 1.268,
    "status": "OPERATING (as of 2007)",
    "cite": {
     "url": "https://amostech.com/TechnicalPapers/2007/Orbital_Debris/Nakajima.pdf",
     "text": "Table 1: phi 250 mm, f 1,268 mm, F/5.1"
    },
    "note": ""
   }
  ],
  "inorbit": [],
  "leftOut": [
   [
    "JASDF deep space radar at Sanyo-Onoda",
    "Wikipedia pages give only city coordinates; no band or limits published in opened pages; status reports conflict (operational March 2025 per one source)"
   ]
  ]
 },
 {
  "slug": "isro-netra",
  "name": "ISRO NETRA / IS4OM",
  "operator": "Indian Space Research Organisation (DSSAM / ISTRAC)",
  "kind": "national program",
  "status": "MOTR commissioned 2015; Chandrapur radar under construction since 2025 (Wikipedia Project NETRA); Hanle telescope under installation (Apr 2026 press)",
  "terms": TERMS_WP,
  "credit": "Wikipedia contributors (CC BY-SA 4.0) and the pages cited per row",
  "sites": [
   {
    "slug": "sdsc-sriharikota-in",
    "name": "Satish Dhawan Space Centre Sriharikota",
    "abbr": "Satish Dhawan Space.",
    "type": "LAUNCH_SITE",
    "kind": "space tracking site",
    "country": "356",
    "lat": 13.72,
    "lon": 80.23,
    "precision": "Wikipedia coordinate of the Satish Dhawan Space Centre, the launch center on Sriharikota island; the Multi-Object Tracking Radar is reported at Sriharikota, its exact spot is not published",
    "where": "Satish Dhawan Space Centre, Sriharikota island, Andhra Pradesh",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Satish_Dhawan_Space_Centre",
     "text": "Infobox coordinates 13 43 N 80 14 E (13.72, 80.23); page says the center is linked to eight radars at Sriharikota"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Indian Space Research Organisation"
     }
    ],
    "status": "OPERATING",
    "statusDate": "2026",
    "note": "Project NETRA page (https://en.wikipedia.org/wiki/Project_NETRA) lists the Sriharikota MOTR, brought online in 2015."
   },
   {
    "slug": "istrac-bengaluru-in",
    "name": "ISTRAC Bengaluru (NETRA control center)",
    "abbr": "ISTRAC Bengaluru (NETRA.",
    "type": "SATELLITE_GROUND_STATION",
    "kind": "space tracking site",
    "country": "356",
    "lat": 12.97889,
    "lon": 77.59167,
    "precision": "Wikipedia coordinate of the city of Bengaluru; the control center is in the city, exact place not published on pages opened",
    "where": "Bengaluru, Karnataka",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Bangalore",
     "text": "City coordinates 12 58 44 N 77 35 30 E"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Indian Space Research Organisation"
     }
    ],
    "status": "OPERATING",
    "statusDate": "2020",
    "note": "Project NETRA page says the SSA Control Centre NETRA was inaugurated in 2020; the ISTRAC network page says Bangalore also hosts the Indian Spacecraft Control Centre (it does not call."
   },
   {
    "slug": "istrac-lucknow-in",
    "name": "ISTRAC Lucknow station",
    "abbr": "ISTRAC Lucknow station",
    "type": "SATELLITE_GROUND_STATION",
    "kind": "space tracking site",
    "country": "356",
    "lat": 26.85,
    "lon": 80.95,
    "precision": "Wikipedia coordinate of the city of Lucknow; the ISTRAC station is listed in the city, exact place not published",
    "where": "Lucknow, Uttar Pradesh",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Lucknow",
     "text": "City coordinates 26 51 N 80 57 E; ISTRAC station place from https://en.wikipedia.org/wiki/ISRO_Telemetry,_Tracking_and_Command_Network"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Indian Space Research Organisation"
     }
    ],
    "status": "UNVERIFIED",
    "note": "Listed as an ISTRAC ground station; no status date or role in space object tracking stated."
   },
   {
    "slug": "istrac-port-blair-in",
    "name": "ISTRAC Port Blair station",
    "abbr": "ISTRAC Port Blair.",
    "type": "SATELLITE_GROUND_STATION",
    "kind": "space tracking site",
    "country": "356",
    "lat": 11.66833,
    "lon": 92.73778,
    "precision": "Wikipedia coordinate of the city of Port Blair (11 40 06 N 92 44 16 E); the station is listed as Sri Vijayapuram (Port Blair), exact place not published",
    "where": "Port Blair, Andaman and Nicobar Islands",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Port_Blair",
     "text": "City coordinates 11 40 06 N 92 44 16 E; ISTRAC station place from https://en.wikipedia.org/wiki/ISRO_Telemetry,_Tracking_and_Command_Network"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Indian Space Research Organisation"
     }
    ],
    "status": "UNVERIFIED",
    "note": "Listed as an ISTRAC ground station only."
   },
   {
    "slug": "mount-abu-eodss-in",
    "name": "Mount Abu Observatory",
    "abbr": "Mount Abu Observatory",
    "type": "ASTRONOMICAL_OBSERVATORY",
    "kind": "space tracking site",
    "country": "356",
    "lat": 24.6548167,
    "lon": 72.7792167,
    "precision": "Wikipedia coordinate of the Mount Abu Observatory (24 39 17.34 N 72 46 45.18 E); the planned telescope is described as part of that observatory, exact spot not published",
    "where": "Mount Abu, Rajasthan",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Mount_Abu_Observatory",
     "text": "Coordinates 24 39 17.34 N 72 46 45.18 E; page says a planned EODSS facility with ISRO LEOS will track space debris with a one-metre telescope"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Physical Research Laboratory / ISRO"
     }
    ],
    "status": "PLANNED",
    "note": "Project NETRA page lists Mount Abu among high-altitude observatories of its optical telescope network, no date."
   }
  ],
  "sensors": [
   {
    "id": "netra-motr-sriharikota",
    "site": "sdsc-sriharikota-in",
    "name": "NETRA Multi-Object Tracking Radar",
    "type": "RADAR_AESA",
    "emission": "ACTIVE",
    "status": "OPERATING",
    "cite": {
     "url": "https://www.indiandefensenews.in/2026/04/india-strengthens-orbital-surveillance.html",
     "text": "Page calls it an L-Band Active Phased Array Radar, 0.25 m2 objects to 1,000 km; Project NETRA Wikipedia page (https://en.wikipedia.org/wiki/Project_NETRA) says the MOTR was commissioned at."
    },
    "note": "Band and active phased array type per the press page opened; no band limits published."
   },
   {
    "id": "istrac-lucknow-ttc",
    "site": "istrac-lucknow-in",
    "name": "ISTRAC Lucknow TT&C antenna",
    "type": "TT_AND_C_ANTENNA",
    "emission": "ACTIVE",
    "status": "UNVERIFIED",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/ISRO_Telemetry,_Tracking_and_Command_Network",
     "text": "Lucknow listed among ISTRAC ground stations"
    },
    "note": "A spacecraft telemetry and command station; the page does not say it observes other space objects."
   },
   {
    "id": "istrac-port-blair-ttc",
    "site": "istrac-port-blair-in",
    "name": "ISTRAC Port Blair TT&C antenna",
    "type": "TT_AND_C_ANTENNA",
    "emission": "ACTIVE",
    "status": "UNVERIFIED",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/ISRO_Telemetry,_Tracking_and_Command_Network",
     "text": "Sri Vijayapuram (Port Blair) listed among ISTRAC ground stations"
    },
    "note": "A spacecraft telemetry and command station; the page does not say it observes other space objects."
   },
   {
    "id": "mount-abu-eodss-1m",
    "site": "mount-abu-eodss-in",
    "name": "Mount Abu EODSS one-metre telescope (planned)",
    "type": "OPTICAL_TELESCOPE",
    "emission": "PASSIVE",
    "apertureM": 1.0,
    "status": "PLANNED",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Mount_Abu_Observatory",
     "text": "Planned EODSS system to use a one-metre telescope to track space debris"
    },
    "note": "Wikipedia wording is planned; no build date."
   }
  ],
  "inorbit": [],
  "leftOut": [
   [
    "MOTR at Sriharikota (L band active phased array, 0.25 m2 RCS to 1000 km)",
    "No page gave coordinates for the radar; Sriharikota island tag (13 42 N, 80 12 E) is the island only. Band is L band with no published limits. Source https://www.indiandefensenews.in/2026/04/india-strengthens-orbital-surveillance.html"
   ],
   [
    "NETRA control centre ISTRAC Bengaluru",
    "No coordinates published on opened pages"
   ],
   [
    "Chandrapur Assam radar (UNDER_CONSTRUCTION), north-east radar (PLANNED), Hanle telescope, Mount Abu, Leh, Ponmudi optical, Nainital Baker-Nunn refurbishment",
    "No facility coordinates; Hanle IAO tag (32.7794 N, 78.9642 E) belongs to a different observatory and the ISRO telescope's location is not confirmed to be there"
   ],
   [
    "NETRA telescope at Hanle",
    "The Indian Astronomical Observatory page (tag 32 46 46 N 78 57 51 E) does not mention a NETRA telescope; Project NETRA page does not mention Hanle"
   ],
   [
    "Chandrapur (Assam) radar",
    "Project NETRA page says construction began in 2025 but gives no coordinates or band"
   ],
   [
    "ISTRAC Sriharikota, Thiruvananthapuram, Mauritius, Brunei, Biak stations",
    "Biak page gives only an island tag (1 S 136 E) and does not mention ISRO; Mauritius and Brunei station places not published on pages opened; Sriharikota already covered by the launch center row; Thiruvananthapuram not fetched"
   ],
   [
    "ARIES Nainital and Devasthal",
    "No opened source states a space-object tracking role"
   ]
  ]
 },
 {
  "slug": "eos-space-systems",
  "name": "EOS Space Systems",
  "operator": "Electro Optic Systems",
  "kind": "commercial",
  "status": "two Australian sites per 2025 brochure",
  "terms": TERMS_UNSTATED,
  "credit": "the pages cited per row",
  "sites": [
   {
    "slug": "eos-mount-stromlo-au",
    "name": "EOS Mount Stromlo laser ranging station",
    "abbr": "EOS Mount Stromlo laser.",
    "type": "OBSERVATION_STATION",
    "kind": "space tracking site",
    "country": "036",
    "lat": -35.3161,
    "lon": 149.0099,
    "alt": 805,
    "precision": "ILRS station page position, printed to four decimals; the page calls the station Mt. Stromlo STL3",
    "where": "Mount Stromlo, Australian Capital Territory",
    "cite": {
     "url": "https://ilrs.gsfc.nasa.gov/network/stations/active/STL3_station_info.html",
     "text": "Latitude 35.3161 S, longitude 149.0099 E, elevation 805.0 m; operator EOS Space Systems Pty. Ltd."
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "EOS Space Systems"
     }
    ],
    "status": "OPERATING",
    "statusDate": "2024",
    "note": "Listed under ILRS active stations; operator EOS Space Systems Pty. Ltd.; site log updated 2024-05-30."
   }
  ],
  "sensors": [
   {
    "id": "eos-stromlo-slr-1m",
    "site": "eos-mount-stromlo-au",
    "name": "EOS Mount Stromlo 1 m laser ranging system",
    "type": "LASER_RANGEFINDER",
    "emission": "ACTIVE",
    "apertureM": 1.0,
    "status": "OPERATING",
    "cite": {
     "url": "https://ilrs.gsfc.nasa.gov/network/stations/active/STL3_station_info.html",
     "text": "Two 1.00 m telescopes (receive and transmit); Nd:YAG 1064 nm primary and 532.1 nm secondary; 60 Hz maximum; listed under Active Stations"
    },
    "note": "Satellite laser ranging station; site log last updated 2024-05-30. Wavelength as published: 1064 nm primary, 532.1 nm secondary; limits in MHz not applicable."
   }
  ],
  "inorbit": [],
  "leftOut": [
   [
    "Mount Stromlo and Learmonth tracking facilities (laser ranging, 0.7 m and 1 m telescopes)",
    "Mount Stromlo Observatory tag exists (35 19 13 S, 149 00 25 E) but the EOS facility's own coordinates and sensor specs were not published; Learmonth has no coordinates"
   ],
   [
    "Learmonth, Western Australia",
    "No page opened publishes a coordinate for the EOS facility; Wikipedia Learmonth Solar Observatory page returned 404"
   ]
  ]
 },
 {
  "slug": "silentium-defence",
  "name": "Silentium Defence",
  "operator": "Silentium Defence",
  "kind": "commercial",
  "status": "unconfirmed",
  "terms": TERMS_WP,
  "credit": "Wikipedia contributors (CC BY-SA 4.0) and the pages cited per row",
  "sites": [
   {
    "slug": "oculus-observatory-swan-reach-au",
    "name": "Oculus Observatory",
    "abbr": "Oculus Observatory",
    "type": "OBSERVATION_STATION",
    "kind": "space tracking site",
    "country": "036",
    "lat": -34.56778,
    "lon": 139.59972,
    "precision": "the town of Swan Reach (Wikipedia coordinate); a search summary of an Australian Space Agency page names Swan Reach while a local news summary says near Brownlow, so the exact place is not published",
    "where": "Mid-Murray region, South Australia, near the River Murray Dark Sky Reserve",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Swan_Reach,_South_Australia",
     "text": "Town coordinates 34 34 04 S 139 35 59 E; observatory region from https://www.hjkc.de/_blog/18582-raumfahrt-south-australia-s-oculus-observatory-hosts-passive-radar-to-track-space-objects"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Silentium Defence"
     }
    ],
    "status": "UNVERIFIED",
    "statusDate": "2021-12",
    "note": "Opening date only; no later status found."
   }
  ],
  "sensors": [
   {
    "id": "silentium-maverick-s-oculus",
    "site": "oculus-observatory-swan-reach-au",
    "name": "MAVERICK S passive radar",
    "type": "PASSIVE_RF_ESM",
    "emission": "PASSIVE",
    "status": "UNVERIFIED",
    "cite": {
     "url": "https://www.hjkc.de/_blog/18582-raumfahrt-south-australia-s-oculus-observatory-hosts-passive-radar-to-track-space-objects",
     "text": "First sensor at the observatory; relies on FM radio transmitters and reflections from space objects"
    },
    "note": "Passive radar using broadcast signals; no own transmitter."
   }
  ],
  "inorbit": [],
  "leftOut": [
   [
    "MAVERICK S observatory-in-a-box; Oculus Observatory, Mid-Murray South Australia",
    "No coordinates, band or current status published"
   ]
  ]
 },
 {
  "slug": "mwa-passive-radar",
  "name": "Murchison Widefield Array passive radar",
  "operator": "MWA collaboration",
  "kind": "research",
  "status": "2013 proof of concept only",
  "terms": TERMS_WP,
  "credit": "Wikipedia contributors (CC BY-SA 4.0) and the pages cited per row",
  "sites": [
   {
    "slug": "mwa-murchison-au",
    "name": "Murchison Radio-astronomy Observatory (MWA)",
    "abbr": "Murchison.",
    "type": "ASTRONOMICAL_OBSERVATORY",
    "kind": "space tracking site",
    "country": "036",
    "lat": -26.7033,
    "lon": 116.671,
    "precision": "Wikipedia coordinate of the Murchison Widefield Array at the Murchison Radio-astronomy Observatory; array center not separately stated",
    "where": "Murchison Radio-astronomy Observatory, Western Australia, about 800 km north of Perth",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Murchison_Widefield_Array",
     "text": "26 42 12 S 116 40 16 E; page lists space debris tracking among Phase I science topics"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "MWA collaboration"
     }
    ],
    "status": "UNVERIFIED",
    "note": "Radio telescope site; the page opened does not state current operating status; passive radar use is a proof of concept (see sensor)."
   }
  ],
  "sensors": [
   {
    "id": "mwa-fm-passive-radar",
    "site": "mwa-murchison-au",
    "name": "MWA FM passive radar demonstration",
    "type": "PASSIVE_RF_ESM",
    "emission": "PASSIVE",
    "status": "UNVERIFIED",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Murchison_Widefield_Array",
     "text": "Space debris tracking listed; FM-reflection detection of the ISS is from the 2013 paper (Tingay et al., Astronomical Journal 146, 103) seen in search results, not opened"
    },
    "note": "Proof of concept in 2013; no operational system found."
   }
  ],
  "inorbit": [],
  "leftOut": []
 },
 {
  "slug": "jasdf-space-radar",
  "name": "JASDF space surveillance radar",
  "operator": "Japan Air Self-Defense Force, Space Operations Group",
  "kind": "national program",
  "status": "status not confirmed on pages opened",
  "terms": TERMS_WP,
  "credit": "Wikipedia contributors (CC BY-SA 4.0) and the pages cited per row",
  "sites": [
   {
    "slug": "jasdf-sanyo-onoda-jp",
    "name": "JASDF space surveillance radar site Sanyo-Onoda",
    "abbr": "JASDF space.",
    "type": "MILITARY_BASE",
    "kind": "space tracking site",
    "country": "392",
    "lat": 34.003056,
    "lon": 131.181944,
    "precision": "city of Sanyo-Onoda (34 00 11 N 131 10 55 E written as decimal); the radar is reported near the city, exact place not published",
    "where": "Sanyo-Onoda, Yamaguchi Prefecture",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Space_Operations_Group",
     "text": "Group combines data from a sensor near Sanyo-Onoda in Yamaguchi Prefecture; city coordinate from https://en.wikipedia.org/wiki/Sanyo-Onoda"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Japan Air Self-Defense Force"
     }
    ],
    "status": "UNVERIFIED",
    "note": "No band or radar type on pages opened; a search summary mentions a possible second radar at Hofu Kita Air Base, not opened."
   }
  ],
  "sensors": [],
  "inorbit": [],
  "leftOut": [
   [
    "Radar sensor row",
    "No band, type or status on pages opened"
   ]
  ]
 },
 {
  "slug": "tfrm-montsec",
  "name": "Telescope Fabra-ROA at Montsec",
  "operator": "Institut d'Estudis Espacials de Catalunya / Real Instituto y Observatorio de la Armada (as named in the paper)",
  "kind": "national program",
  "status": "operating (paper reports space-debris GEO surveys; no current status page opened)",
  "terms": TERMS_UNSTATED,
  "credit": "the pages cited per row",
  "sites": [
   {
    "slug": "oadm-montsec",
    "name": "Observatori Astronomic del Montsec",
    "abbr": "Observatori Astronomic.",
    "type": "ASTRONOMICAL_OBSERVATORY",
    "kind": "space tracking site",
    "country": "724",
    "lat": 42.0516,
    "lon": 0.7293,
    "alt": 1570,
    "precision": "WGS84 coordinates published in the paper for the observatory where the telescope is installed (decimal degrees as published)",
    "where": "Montsec, Catalan Pre-Pyrenees, Lleida, Spain",
    "cite": {
     "url": "https://arxiv.org/pdf/1211.5581",
     "text": "Fors et al. 2013 PASP 125:522: phi=42.0516 N, lambda=0.7293 E, h=1570 m"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Fabra-ROA Montsec"
     }
    ],
    "status": "OPERATING",
    "statusDate": "2013",
    "note": "Coordinates, 1570 m, 0.5 m f/0.96 and GEO debris surveys verified in the paper text. Status is as of the 2013 paper only."
   }
  ],
  "sensors": [
   {
    "id": "tfrm-bnc",
    "site": "oadm-montsec",
    "name": "TFRM (Fabra-ROA Baker-Nunn camera)",
    "type": "OPTICAL_TELESCOPE",
    "emission": "PASSIVE",
    "apertureM": 0.5,
    "status": "OPERATING",
    "cite": {
     "url": "https://arxiv.org/pdf/1211.5581",
     "text": "0.5 m aperture f/0.96 modified Baker-Nunn, 4.4x4.4 deg field"
    },
    "note": "per 2013 paper"
   }
  ],
  "inorbit": [],
  "leftOut": [
   [
    "ROA San Fernando, Centu-1 and Deimos Sky Survey (Puertollano), S3TSR at Moron",
    "no page with facility coordinates or sensor specs opened in budget; S3TSR site not confirmed"
   ]
  ]
 },
 {
  "slug": "aiub-zimmerwald",
  "name": "AIUB Zimmerwald",
  "operator": "Astronomical Institute, University of Bern",
  "kind": "university",
  "status": "operating",
  "terms": TERMS_WP,
  "credit": "Wikipedia contributors (CC BY-SA 4.0) and the pages cited per row",
  "sites": [
   {
    "slug": "zimmerwald",
    "name": "Zimmerwald Observatory",
    "abbr": "Zimmerwald Observatory",
    "type": "ASTRONOMICAL_OBSERVATORY",
    "kind": "space tracking site",
    "country": "756",
    "lat": 46.87667,
    "lon": 7.465,
    "precision": "Wikipedia coordinate tag on the observatory's own article",
    "where": "Zimmerwald, Bern, Switzerland",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Zimmerwald_Observatory",
     "text": "46.87667 N 7.46500 E"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "University of Bern"
     }
    ],
    "status": "OPERATING",
    "note": "Present-tense university description; no status date."
   }
  ],
  "sensors": [
   {
    "id": "zimlat",
    "site": "zimmerwald",
    "name": "ZIMLAT 1 m",
    "type": "LASER_RANGEFINDER",
    "emission": "ACTIVE",
    "apertureM": 1,
    "status": "OPERATING",
    "cite": {
     "url": "https://www.aiub.unibe.ch/research/zimmerwald_observatory/telescopes/index_eng.html",
     "text": "1 m laser and astrometry telescope since 1997; SLR plus CCD optical"
    },
    "note": ""
   },
   {
    "id": "zimsmart",
    "site": "zimmerwald",
    "name": "ZimSMART 0.2 m",
    "type": "OPTICAL_TELESCOPE",
    "emission": "PASSIVE",
    "apertureM": 0.2,
    "status": "OPERATING",
    "cite": {
     "url": "https://www.aiub.unibe.ch/research/zimmerwald_observatory/telescopes/index_eng.html",
     "text": "20 cm, f=0.5 m, 4.2 deg field, SMARTnet"
    },
    "note": ""
   }
  ],
  "inorbit": [],
  "leftOut": [
   [
    "SMARTnet stations (Australia, South Africa, Chile)",
    "no per-station coordinates opened; outside Europe"
   ]
  ]
 },
 {
  "slug": "thothx",
  "name": "ThothX deep space radar",
  "operator": "ThothX (Thoth Technology)",
  "kind": "commercial",
  "status": "active",
  "terms": TERMS_WP,
  "credit": "Wikipedia contributors (CC BY-SA 4.0) and the pages cited per row",
  "sites": [
   {
    "slug": "algonquin-radio-observatory",
    "name": "Algonquin Radio Observatory",
    "abbr": "Algonquin Radio.",
    "type": "RESEARCH_FACILITY",
    "kind": "space tracking site",
    "country": "124",
    "lat": 45.9555,
    "lon": -78.073,
    "precision": "Coordinate tag on the facility's own Wikipedia article (the observatory site, which hosts the 46 m antenna).",
    "where": "Ontario, Canada",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/Algonquin_Radio_Observatory",
     "text": "Wikipedia geo tag 45.9555, -78.073; 46 m main antenna; operated by Thoth Technology since 2007"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "Thoth Technology"
     }
    ],
    "status": "OPERATING",
    "statusDate": "2018 (AGI press release; later status not confirmed)",
    "note": ""
   },
   {
    "slug": "carnarvon-otc-dish",
    "name": "OTC Satellite Earth Station Carnarvon",
    "abbr": "OTC Satellite Earth.",
    "type": "RESEARCH_FACILITY",
    "kind": "space tracking site",
    "country": "036",
    "lat": -24.86917,
    "lon": 113.70472,
    "precision": "Wikipedia coordinate tag on the station's own article, 24 52 09 S 113 42 17 E written as decimal degrees (the former earth station that holds the dish).",
    "where": "Carnarvon, Western Australia",
    "cite": {
     "url": "https://en.wikipedia.org/wiki/OTC_Satellite_Earth_Station_Carnarvon",
     "text": "Wikipedia: ThothX Australia acquired the antenna in 2022 and is refurbishing it into a deep space radar; no date given"
    },
    "parties": [
     {
      "role": "OPERATOR",
      "name": "ThothX Australia"
     }
    ],
    "status": "UNDER_CONSTRUCTION",
    "statusDate": "2022 acquisition; refurbishment ongoing per Wikipedia",
    "note": ""
   }
  ],
  "sensors": [
   {
    "id": "thothx-algonquin-radar",
    "site": "algonquin-radio-observatory",
    "name": "ARO 46 m deep space radar",
    "type": "RADAR_PULSE",
    "emission": "ACTIVE",
    "apertureM": 46,
    "status": "OPERATING",
    "cite": {
     "url": "https://www.agi.com/blog/2018/05/agi-and-thoth-technology-announce-world-s-first-co",
     "text": "AGI blog: 46 m antenna at ARO, calibrated and collecting observations of objects in and crossing GEO; range up to 50,000 km"
    },
    "note": "Type is a generic radar choice; waveform not published. Range stated up to 50,000 km by Thoth CEO. Status from 2018 release."
   },
   {
    "id": "thothx-carnarvon-radar",
    "site": "carnarvon-otc-dish",
    "name": "Carnarvon deep space radar (planned from OTC dish)",
    "type": "RADAR_PULSE",
    "emission": "ACTIVE",
    "status": "UNDER_CONSTRUCTION",
    "cite": {
     "url": "https://www.midwesttimes.com.au/news/midwest-times/historic-carnarvon-satellite-dish-finds-new-life-as-deep-space-radar-tracking-spacecraft-orbiting-earth-c-9137613",
     "text": "Midwest Times 14 Dec 2022: dish being upgraded for commercial space tracking under a deal between Shire of Carnarvon and ThothX"
    },
    "note": "Being refurbished for commercial space tracking; Wikipedia gives 12.8 m Casshorn and 29.8 m dishes at the station but does not say which is used."
   }
  ],
  "inorbit": [],
  "leftOut": [
   [
    "Mid-2026 status of both ThothX radars",
    "Newest source read is 2022-2023 press"
   ]
  ]
 },
 {
  "slug": "japan-inorbit",
  "name": "Japan in-orbit sensors",
  "operator": "Astroscale / JAXA",
  "kind": "commercial",
  "status": "unknown",
  "terms": TERMS_UNSTATED,
  "credit": "the pages cited per row",
  "sites": [],
  "sensors": [],
  "inorbit": [
   {
    "id": "adras-j",
    "name": "ADRAS-J rendezvous sensors",
    "norad": 58992,
    "host": "ADRAS-J",
    "type": "OPTICAL_TELESCOPE",
    "emission": "PASSIVE",
    "status": "Listed operational on keeptrack page; mission ongoing as of April 2024 per Astroscale; later status not verified",
    "cite": {
     "url": "https://www.astroscale.com/en/news/astroscale-unveils-worlds-first-image-of-space-debris-captured-through-rendezvous-and-proximity-operations",
     "text": "Astroscale: first image of space debris captured through rendezvous and proximity operations"
    },
    "verified": "public catalog page: https://keeptrack.space/satellite/58992 (NORAD 58992, 2024-034A)",
    "note": "Astroscale page says it imaged a Japanese upper-stage rocket body via a suite of rendezvous payload sensors; camera type and the object catalog number are not on the page opened."
   }
  ],
  "leftOut": []
 }
];

// Programs the research named for which no row could be published, and why (nothing is published for these).
export const NOT_PUBLISHED = [
 {
  "slug": "roscosmos-aspos",
  "name": "Roscosmos ASPOS OKP",
  "leftOut": [
   [
    "ASPOS OKP sites (AZT-33VM, Sayan AZT-33IK, Titov Altai center)",
    "RussianSpaceWeb names them with no coordinates and no post-2013 status; the Altay center is recorded under russia"
   ]
  ]
 },
 {
  "slug": "apsco-aposos",
  "name": "APSCO APOSOS optical network",
  "leftOut": [
   [
    "APOSOS stations in Peru, Turkey, China, Thailand, Bangladesh, Mongolia, Pakistan, Iran (Indonesia signatory); first batch of 15 cm refractor debris telescopes from CIOMP; Iran's at Mahdasht Space Centre expected Oct 2016",
    "Known only from search-result snippets; the NAOC and researching.cn pages failed certificate checks and the SWF PDFs were 404. No coordinates, apertures or status read from an opened page."
   ]
  ]
 },
 {
  "slug": "israel",
  "name": "Israel space surveillance",
  "leftOut": [
   [
    "IAI ELM-2097 Space Guard S-band LEO radar",
    "IAI product page only; no installed site or coordinates published."
   ],
   [
    "KASI OWL-Net station at Mitzpe Ramon (MPC code M33, 0.5 m)",
    "Korean-run network; station coordinates not on pages opened (PMC article gives none; Mitzpe Ramon Wikipedia tag 30 36 46 N 34 48 14 E is the town only). Belongs to the Korean program."
   ],
   [
    "Green Pine / Arrow radars",
    "No source found for satellite tracking."
   ],
   [
    "Ground sensors of Israel",
    "Search returned only Ofek imaging satellites and Palmachim launches; no radar or telescope with a place."
   ]
  ]
 },
 {
  "slug": "uae",
  "name": "UAE space situational awareness",
  "leftOut": [
   [
    "UAE University 256-element radio-array observatory for SSA, Al Ain",
    "Page (nsstc.uaeu.ac.ae) says it will be established (planned); no coordinates, no band."
   ],
   [
    "Sharjah Optical Observatory; Abu Dhabi 0.8 m ground station",
    "Seen only in search snippets; Sharjah page returned 404; no coordinates."
   ],
   [
    "Sharjah observatory",
    "Page 404; no coordinate."
   ],
   [
    "MBRSC ground station",
    "No SSA or coordinate found."
   ],
   [
    "UAEU Al Ain site and 256-element radio array",
    "NSSTC page says the array is planned and will deploy from the NSSTC-UAEU platform but does not name Al Ain or any site for it; the Al Ain location is only implied. Site and sensor removed."
   ]
  ]
 },
 {
  "slug": "morocco",
  "name": "Morocco hosted",
  "leftOut": [
   [
    "KASI OWL-Net station at Oukaimeden",
    "PMC article gives no coordinates; Oukaimeden Wikipedia page returned 404; belongs to the Korean program."
   ]
  ]
 },
 {
  "slug": "bolivia",
  "name": "Bolivia hosted",
  "leftOut": [
   [
    "ISON site at Tarija",
    "Wikipedia ISON article lists the place with no coordinates, aperture or status."
   ]
  ]
 },
 {
  "slug": "digantara",
  "name": "Digantara ground observatory",
  "leftOut": [
   [
    "Digantara optical observatory, Uttarakhand",
    "No coordinates or current status published in opened results"
   ],
   [
    "Ground observatory, Garhwal, Uttarakhand",
    "Only search summaries; no coordinates and no confirmation it was built"
   ],
   [
    "SCOT satellite",
    "No catalog number seen on an opened public catalog page"
   ]
  ]
 },
 {
  "slug": "other-asia-pacific",
  "name": "Other Asia-Pacific",
  "leftOut": [
   [
    "Murchison Widefield Array passive FM radar",
    "Only a 2013 proof-of-concept paper found; no operational status or limits"
   ],
   [
    "NARIT 0.7 m TNSO telescope, Doi Inthanon (Thailand)",
    "Station is about 2 km from the Thai National Observatory (tag 18 35 26 N, 98 29 12 E), not inside it; no station coordinates"
   ],
   [
    "Taiwan, New Zealand (Mt John; LeoLabs Kiwi already published)",
    "No dedicated space surveillance sensor with published facts found"
   ],
   [
    "Korea KARI Goheung and Jeju tracking radars",
    "Launch-vehicle tracking radars, no coordinates or band; no Korean SSA radar is operating"
   ]
  ]
 },
 {
  "slug": "commercial-ground-unplaced",
  "name": "Other commercial",
  "leftOut": [
   [
    "Slingshot/Numerica, ExoAnalytic, Spaceflux, Aldoria, Vyoma, Sybilla, Safran WeTrack, Kratos, NorthStar, HEO",
    "No opened page gave a site place with the sensor; Slingshot only says about 20 sites worldwide; ExoAnalytic Wikipedia page 404"
   ],
   [
    "Look Up SORASYS, Monts-de-Randon, Lozere",
    "Place and 1,200 m altitude seen in search summary only; press pages returned 403"
   ],
   [
    "ExoAnalytic Global Network (350+ telescopes)",
    "Own pages give counts only; no site list or coordinates found"
   ],
   [
    "Slingshot Global Sensor Network (former Numerica, 20+ sites)",
    "Sources name counts and regions only; no per-site coordinates"
   ],
   [
    "Kratos RF monitoring (about 20 sites)",
    "Counts by region only; no site names or coordinates"
   ],
   [
    "Aldoria / Share My Space",
    "Only third-party profiles list countries (France, Spain, Belgium, Chile); no coordinates"
   ],
   [
    "Spaceflux, Safran WeTrack, EOS, Sybilla / 6ROADS, Vyoma ground sites",
    "No usable page found within budget"
   ]
  ]
 },
 {
  "slug": "ukraine",
  "name": "Ukraine",
  "leftOut": [
   [
    "Mukachevo Dnepr radar",
    "Wikipedia tag 48.37768 N 22.70744 E exists, but source says only that it was to be closed then used part-time for space surveillance; current status and band not published"
   ],
   [
    "Evpatoria RT-70 (Crimea)",
    "tag 45.19 N 33.19 E is minutes-level; located in Russian-occupied Crimea; Wikipedia reports the dish struck by a drone Aug 2025; no radar limits; status unclear"
   ],
   [
    "Mykolaiv optical",
    "not researched"
   ]
  ]
 },
 {
  "slug": "china-inorbit",
  "name": "China in-orbit sensors",
  "leftOut": [
   [
    "Shijian-17 (41838), Shijian-21 (49330), TJS-3, TJS series",
    "Catalog numbers seen in search results but no cited source states an object-observing sensor"
   ],
   [
    "Shiyan-6",
    "A search summary says its designer described a pilot GEO space situational awareness capability; the page could not be opened, so no row"
   ]
  ]
 },
 {
  "slug": "russia-inorbit",
  "name": "Russia in-orbit sensors",
  "leftOut": [
   [
    "Kosmos 2576 (59773) and other inspector satellites; Luch/Olymp",
    "No cited source opened that states an observing sensor"
   ]
  ]
 },
 {
  "slug": "other-inorbit",
  "name": "Other in-orbit sensors not placed",
  "leftOut": [
   [
    "Indian, ESA/European, Scout Space, Starfish spacecraft",
    "Not researched within budget"
   ],
   [
    "Iranian, other national sensor networks (user request)",
    "Outside this assignment lane"
   ]
  ]
 }
];

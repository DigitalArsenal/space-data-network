package sds

// unguardedEmbeddedSchemas are embedded IDLs the node does NOT decode field-by-
// field today, so no typed binding instance exists to compare them against.
//
// This is a DECLARATION, not a dismissal. The embed still ships in the binary
// and the strict validator still reads it, so any of these can drift from the
// pinned bindings without a test noticing — the RFB defect
// (sdn-server-rfb-schema-embed-stale) is exactly that failure. CES.fbs used to
// be the live example — it carried `cesPoolingKind.MAX`, which SDS removed at
// 1.183.0 as a flatc sentinel collision, and the node sat behind that at
// lib/go v1.177.0. The v1.186.0 pin bump carries the node past it: embed and
// bindings are now both post-1.183.0 and the sentinel is gone from each.
//
// $IRM (Ingest Resume Mark, REC ordinal 223) arrives waived on the v1.196.0
// pin for the same reason and no other: the ingest resume mark is written by
// WASM through the schema-typed storage.write capability and read back by the
// module that wrote it, so no Go binding instance decodes it here. It is NOT
// waived quietly — TestIRMIsAdmittedByTheEmbeddedValidator exercises the embed
// against the vendored IRM binding end to end, which is the compensating
// control this waiver would otherwise lack. The moment the host reads an IRM
// field in Go, that test stops being enough and the rule below applies.
//
// $VCF (vCard Projection Card, REC ordinal 224) arrives waived on the v1.197.0
// pin under the same single reason. A $VCF record is produced by the vCard
// PROJECTION MODULE — WASM — and written through the schema-typed storage.write
// capability; the Go host emits vCard TEXT from an EPM (internal/epm,
// internal/auth) but never decodes a $VCF FlatBuffer field, so there is no
// binding instance here to compare the embed against. The compensating control
// is TestVCFIsAdmittedByTheEmbeddedValidator, which drives a real card through
// the embedded validator with the VENDORED binding. If the host ever reads a
// $VCF field in Go — say to serve a card it did not project — that test stops
// being enough and the rule below applies.

// $STX (Scheduled Transmission, REC 225) and $TXS (Terrestrial Transmitter
// Site, REC 226) arrive waived together on the v1.198.0 pin, and they are ONE
// waiver rather than two: STX.fbs includes ../TXS/main.fbs and its SOURCES
// vector is [TXSProvenance], so the pair is a single include closure that the
// validator either loads whole or not at all. The reason is the standing one —
// the RF-catalog ingest builds these records in WASM and writes them through
// the schema-typed storage.write capability; no Go binding instance here
// decodes a $TXS or $STX field. The compensating control is
// TestTXSAndSTXAreAdmittedByTheEmbeddedValidator, which drives real records of
// BOTH standards through the embedded validator with the VENDORED bindings and
// asserts the TXS.ID <- STX.SITE_ID join the RF dataset is read on. The moment
// the host reads one of these fields in Go, the rule below applies.

// THE MOMENT THE HOST STARTS READING ONE OF THESE, move it to
// driftGuardedSchemas. That is one line and it is not optional.
var unguardedEmbeddedSchemas = map[string]bool{
	// $ICN / $TRP / $TRV (v1.207.0 mint, dashboard-console program): the host
	// decodes $TRP and $TRV in internal/trust with the pinned bindings and
	// round-trips both in trust tests (rules_test.go), which is the compensating
	// control until they graduate to driftGuardedSchemas; $ICN is written by the
	// ingest lanes that land after this pin.
	"ICN.fbs":  true,
	"TRP.fbs":  true,
	"TRV.fbs":  true,
	"ACI.fbs":  true,
	"ACL.fbs":  true,
	"ACM.fbs":  true,
	"ACR.fbs":  true,
	"ACW.fbs":  true,
	"AEM.fbs":  true,
	"ANI.fbs":  true,
	"AOF.fbs":  true,
	"APL.fbs":  true,
	"APM.fbs":  true,
	"ARM.fbs":  true,
	"AST.fbs":  true,
	"ATD.fbs":  true,
	"ATM.fbs":  true,
	"AVL.fbs":  true,
	"BAL.fbs":  true,
	"BEM.fbs":  true,
	"BMC.fbs":  true,
	"BOV.fbs":  true,
	"BSP.fbs":  true,
	"BUS.fbs":  true,
	"CAQ.fbs":  true,
	"CDM.fbs":  true,
	"CES.fbs":  true,
	"CFP.fbs":  true,
	"CHN.fbs":  true,
	"CLT.fbs":  true,
	"CMR.fbs":  true,
	"CMS.fbs":  true,
	"CMT.fbs":  true,
	"COM.fbs":  true,
	"COT.fbs":  true,
	"CPS.fbs":  true,
	"CRD.fbs":  true,
	"CRM.fbs":  true,
	"CSM.fbs":  true,
	"CTR.fbs":  true,
	"CVG.fbs":  true,
	"CVP.fbs":  true,
	"CZM.fbs":  true,
	"DFH.fbs":  true,
	"DMG.fbs":  true,
	"DOA.fbs":  true,
	"DPM.fbs":  true,
	"DSS.fbs":  true,
	"DTT.fbs":  true,
	"EMC.fbs":  true,
	"EME.fbs":  true,
	"ENC.fbs":  true,
	"ENT.fbs":  true,
	"ENV.fbs":  true,
	"EOO.fbs":  true,
	"EOP.fbs":  true,
	"EPF.fbs":  true,
	"EPM.fbs":  true,
	"ESL.fbs":  true,
	"ETM.fbs":  true,
	"EWR.fbs":  true,
	"FCS.fbs":  true,
	"FPC.fbs":  true,
	"FRM.fbs":  true,
	"FSB.fbs":  true,
	"FSM.fbs":  true,
	"FSO.fbs":  true,
	"FSP.fbs":  true,
	"GDI.fbs":  true,
	"GEL.fbs":  true,
	"GEO.fbs":  true,
	"GJN.fbs":  true,
	"GNO.fbs":  true,
	"GNP.fbs":  true,
	"GPX.fbs":  true,
	"GRV.fbs":  true,
	"GST.fbs":  true,
	"GVH.fbs":  true,
	"HEL.fbs":  true,
	"HFC.fbs":  true,
	"HYP.fbs":  true,
	"IDM.fbs":  true,
	"ION.fbs":  true,
	"IRM.fbs":  true,
	"IRO.fbs":  true,
	"KMF.fbs":  true,
	"KML.fbs":  true,
	"KRF.fbs":  true,
	"LAM.fbs":  true,
	"LCC.fbs":  true,
	"LCF.fbs":  true,
	"LCH.fbs":  true,
	"LDM.fbs":  true,
	"LGR.fbs":  true,
	"LMO.fbs":  true,
	"LMR.fbs":  true,
	"LMS.fbs":  true,
	"LND.fbs":  true,
	"LNE.fbs":  true,
	"LPF.fbs":  true,
	"LWK.fbs":  true,
	"MBL.fbs":  true,
	"MDP.fbs":  true,
	"MDS.fbs":  true,
	"MET.fbs":  true,
	"MFE.fbs":  true,
	"MNF.fbs":  true,
	"MNV.fbs":  true,
	"MSL.fbs":  true,
	"MST.fbs":  true,
	"MTI.fbs":  true,
	"NAV.fbs":  true,
	"NUM.fbs":  true,
	"OBD.fbs":  true,
	"OBT.fbs":  true,
	"OCM.fbs":  true,
	"OEM.fbs":  true,
	"OOA.fbs":  true,
	"OOB.fbs":  true,
	"OOD.fbs":  true,
	"OOE.fbs":  true,
	"OOI.fbs":  true,
	"OOL.fbs":  true,
	"OON.fbs":  true,
	"OOS.fbs":  true,
	"OOT.fbs":  true,
	"OPM.fbs":  true,
	"OPP.fbs":  true,
	"OSM.fbs":  true,
	"PAP.fbs":  true,
	"PCF.fbs":  true,
	"PGM.fbs":  true,
	"PGR.fbs":  true,
	"PHY.fbs":  true,
	"PIV.fbs":  true,
	"PKB.fbs":  true,
	"PLD.fbs":  true,
	"PLHD.fbs": true,
	"PLK.fbs":  true,
	"PLOG.fbs": true,
	"PNL.fbs":  true,
	"PPE.fbs":  true,
	"PRG.fbs":  true,
	"PRR.fbs":  true,
	"PRW.fbs":  true,
	"PUR.fbs":  true,
	"QEM.fbs":  true,
	"RAF.fbs":  true,
	"RBK.fbs":  true,
	"RCF.fbs":  true,
	"RDM.fbs":  true,
	"RDO.fbs":  true,
	"REC.fbs":  true,
	"REM.fbs":  true,
	"REV.fbs":  true,
	"RFE.fbs":  true,
	"RFL.fbs":  true,
	"RFM.fbs":  true,
	"RFO.fbs":  true,
	"RFS.fbs":  true,
	"RHD.fbs":  true,
	"ROC.fbs":  true,
	"RPT.fbs":  true,
	"RSD.fbs":  true,
	"SAR.fbs":  true,
	"SBM.fbs":  true,
	"SCC.fbs":  true,
	"SCM.fbs":  true,
	"SCN.fbs":  true,
	"SCV.fbs":  true,
	"SCX.fbs":  true,
	"SDF.fbs":  true,
	"SDL.fbs":  true,
	"SDR.fbs":  true,
	"SEN.fbs":  true,
	"SEO.fbs":  true,
	"SEV.fbs":  true,
	"SHC.fbs":  true,
	"SHW.fbs":  true,
	"SIT.fbs":  true,
	"SKI.fbs":  true,
	"SNR.fbs":  true,
	"SNW.fbs":  true,
	"SOI.fbs":  true,
	"SON.fbs":  true,
	"SPP.fbs":  true,
	"SRI.fbs":  true,
	"STO.fbs":  true,
	"STR.fbs":  true,
	"STV.fbs":  true,
	"STX.fbs":  true,
	"SUB.fbs":  true,
	"SWR.fbs":  true,
	"TAB.fbs":  true,
	"TBS.fbs":  true,
	"TCF.fbs":  true,
	"TDM.fbs":  true,
	"TFN.fbs":  true,
	"TIM.fbs":  true,
	"TKG.fbs":  true,
	"TME.fbs":  true,
	"TMF.fbs":  true,
	"TMS.fbs":  true,
	"TNR.fbs":  true,
	"TPN.fbs":  true,
	"TRE.fbs":  true,
	"TRK.fbs":  true,
	"TRN.fbs":  true,
	"TRS.fbs":  true,
	"TXS.fbs":  true,
	"VAM.fbs":  true,
	"VCF.fbs":  true,
	"VCM.fbs":  true,
	"VEP.fbs":  true,
	"VST.fbs":  true,
	"WKS.fbs":  true,
	"WPN.fbs":  true,
	"WTH.fbs":  true,
	"XTC.fbs":  true,
}

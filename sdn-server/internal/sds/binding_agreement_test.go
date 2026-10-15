package sds

import (
	"encoding/binary"
	"reflect"
	"testing"

	sdsACL "github.com/DigitalArsenal/spacedatastandards.org/lib/go/ACL"
	sdsAPP "github.com/DigitalArsenal/spacedatastandards.org/lib/go/APP"
	sdsCAT "github.com/DigitalArsenal/spacedatastandards.org/lib/go/CAT"
	sdsCCT "github.com/DigitalArsenal/spacedatastandards.org/lib/go/CCT"
	sdsCLM "github.com/DigitalArsenal/spacedatastandards.org/lib/go/CLM"
	sdsCNP "github.com/DigitalArsenal/spacedatastandards.org/lib/go/CNP"
	sdsCQR "github.com/DigitalArsenal/spacedatastandards.org/lib/go/CQR"
	sdsDPM "github.com/DigitalArsenal/spacedatastandards.org/lib/go/DPM"
	sdsDSS "github.com/DigitalArsenal/spacedatastandards.org/lib/go/DSS"
	sdsEGP "github.com/DigitalArsenal/spacedatastandards.org/lib/go/EGP"
	sdsENC "github.com/DigitalArsenal/spacedatastandards.org/lib/go/ENC"
	sdsENT "github.com/DigitalArsenal/spacedatastandards.org/lib/go/ENT"
	sdsEPM "github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	sdsFSM "github.com/DigitalArsenal/spacedatastandards.org/lib/go/FSM"
	sdsFSP "github.com/DigitalArsenal/spacedatastandards.org/lib/go/FSP"
	sdsICN "github.com/DigitalArsenal/spacedatastandards.org/lib/go/ICN"
	sdsIQC "github.com/DigitalArsenal/spacedatastandards.org/lib/go/IQC"
	sdsIRM "github.com/DigitalArsenal/spacedatastandards.org/lib/go/IRM"
	sdsKMF "github.com/DigitalArsenal/spacedatastandards.org/lib/go/KMF"
	sdsLCF "github.com/DigitalArsenal/spacedatastandards.org/lib/go/LCF"
	sdsLCH "github.com/DigitalArsenal/spacedatastandards.org/lib/go/LCH"
	sdsLDM "github.com/DigitalArsenal/spacedatastandards.org/lib/go/LDM"
	sdsLGR "github.com/DigitalArsenal/spacedatastandards.org/lib/go/LGR"
	sdsLKS "github.com/DigitalArsenal/spacedatastandards.org/lib/go/LKS"
	sdsLPF "github.com/DigitalArsenal/spacedatastandards.org/lib/go/LPF"
	sdsMBL "github.com/DigitalArsenal/spacedatastandards.org/lib/go/MBL"
	sdsMPE "github.com/DigitalArsenal/spacedatastandards.org/lib/go/MPE"
	sdsNCD "github.com/DigitalArsenal/spacedatastandards.org/lib/go/NCD"
	sdsOEM "github.com/DigitalArsenal/spacedatastandards.org/lib/go/OEM"
	sdsOMM "github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"
	sdsPGM "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PGM"
	sdsPIV "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PIV"
	sdsPLG "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PLG"
	sdsPMM "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PMM"
	sdsPNM "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"
	sdsPRR "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PRR"
	sdsPUR "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PUR"
	sdsREV "github.com/DigitalArsenal/spacedatastandards.org/lib/go/REV"
	sdsRFB "github.com/DigitalArsenal/spacedatastandards.org/lib/go/RFB"
	sdsSPW "github.com/DigitalArsenal/spacedatastandards.org/lib/go/SPW"
	sdsSTF "github.com/DigitalArsenal/spacedatastandards.org/lib/go/STF"
	sdsSTX "github.com/DigitalArsenal/spacedatastandards.org/lib/go/STX"
	sdsTBS "github.com/DigitalArsenal/spacedatastandards.org/lib/go/TBS"
	sdsTNR "github.com/DigitalArsenal/spacedatastandards.org/lib/go/TNR"
	sdsTRE "github.com/DigitalArsenal/spacedatastandards.org/lib/go/TRE"
	sdsTRP "github.com/DigitalArsenal/spacedatastandards.org/lib/go/TRP"
	sdsTRV "github.com/DigitalArsenal/spacedatastandards.org/lib/go/TRV"
	sdsTXS "github.com/DigitalArsenal/spacedatastandards.org/lib/go/TXS"
	sdsVCF "github.com/DigitalArsenal/spacedatastandards.org/lib/go/VCF"
	sdsWXF "github.com/DigitalArsenal/spacedatastandards.org/lib/go/WXF"
)

// boundRoots maps every Space Data Standard this node reads through a
// published Go binding to that binding's root accessor. The structural
// verifier walks a buffer by the IDL; the node reads it through the binding.
// If the two disagree about a slot, the verifier checks one thing and the
// accessor reads another, and NET-01 is back.
var boundRoots = map[string]func([]byte) reflect.Value{
	"ACL.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsACL.GetRootAsACL(b, 0)) },
	"APP.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsAPP.GetRootAsAPP(b, 0)) },
	"CAT.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsCAT.GetRootAsCAT(b, 0)) },
	"CCT.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsCCT.GetRootAsCCT(b, 0)) },
	"CLM.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsCLM.GetRootAsCLM(b, 0)) },
	"CNP.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsCNP.GetRootAsCNP(b, 0)) },
	"CQR.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsCQR.GetRootAsCQR(b, 0)) },
	"DPM.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsDPM.GetRootAsDPM(b, 0)) },
	"DSS.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsDSS.GetRootAsDSS(b, 0)) },
	"EGP.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsEGP.GetRootAsEGP(b, 0)) },
	"ENC.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsENC.GetRootAsENC(b, 0)) },
	"ENT.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsENT.GetRootAsENT(b, 0)) },
	"EPM.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsEPM.GetRootAsEPM(b, 0)) },
	"FSM.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsFSM.GetRootAsFSM(b, 0)) },
	"FSP.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsFSP.GetRootAsFSP(b, 0)) },
	"ICN.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsICN.GetRootAsICN(b, 0)) },
	"IQC.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsIQC.GetRootAsIQC(b, 0)) },
	"IRM.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsIRM.GetRootAsIRM(b, 0)) },
	"KMF.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsKMF.GetRootAsKMF(b, 0)) },
	"LCF.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsLCF.GetRootAsLCF(b, 0)) },
	"LCH.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsLCH.GetRootAsLCH(b, 0)) },
	"LDM.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsLDM.GetRootAsLDM(b, 0)) },
	"LGR.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsLGR.GetRootAsLGR(b, 0)) },
	"LKS.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsLKS.GetRootAsLKS(b, 0)) },
	"LPF.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsLPF.GetRootAsLPF(b, 0)) },
	"MBL.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsMBL.GetRootAsMBL(b, 0)) },
	"MPE.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsMPE.GetRootAsMPE(b, 0)) },
	"NCD.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsNCD.GetRootAsNCD(b, 0)) },
	"OEM.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsOEM.GetRootAsOEM(b, 0)) },
	"OMM.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsOMM.GetRootAsOMM(b, 0)) },
	"PGM.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsPGM.GetRootAsPGM(b, 0)) },
	"PIV.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsPIV.GetRootAsPIV(b, 0)) },
	"PLG.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsPLG.GetRootAsPLG(b, 0)) },
	"PMM.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsPMM.GetRootAsPMM(b, 0)) },
	"PNM.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsPNM.GetRootAsPNM(b, 0)) },
	"PRR.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsPRR.GetRootAsPRR(b, 0)) },
	"PUR.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsPUR.GetRootAsPUR(b, 0)) },
	"REV.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsREV.GetRootAsREV(b, 0)) },
	"RFB.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsRFB.GetRootAsRFB(b, 0)) },
	"SPW.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsSPW.GetRootAsSPW(b, 0)) },
	"STF.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsSTF.GetRootAsSTF(b, 0)) },
	"STX.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsSTX.GetRootAsSTX(b, 0)) },
	"TBS.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsTBS.GetRootAsTBS(b, 0)) },
	"TNR.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsTNR.GetRootAsTNR(b, 0)) },
	"TRE.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsTRE.GetRootAsTRE(b, 0)) },
	"TRP.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsTRP.GetRootAsTRP(b, 0)) },
	"TRV.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsTRV.GetRootAsTRV(b, 0)) },
	"TXS.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsTXS.GetRootAsTXS(b, 0)) },
	"VCF.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsVCF.GetRootAsVCF(b, 0)) },
	"WXF.fbs": func(b []byte) reflect.Value { return reflect.ValueOf(sdsWXF.GetRootAsWXF(b, 0)) },
}

// slotProbe builds a bare buffer for schema ident whose root table's vtable
// has slotCount slots, all absent except slot k. With far=true slot k's
// field sits 64 KiB past the end of the buffer; otherwise it sits inside the
// table and holds a uoffset that points 2 GiB past the end.
func slotProbe(ident string, slotCount, k int, far bool) []byte {
	const fieldArea = 64
	vt := 8
	vsize := 4 + 2*slotCount
	table := vt + vsize
	table += (4 - table%4) % 4
	tsize := 4 + fieldArea
	buf := make([]byte, table+tsize+512)
	binary.LittleEndian.PutUint32(buf[0:], uint32(table))
	copy(buf[4:8], ident)
	binary.LittleEndian.PutUint16(buf[vt:], uint16(vsize))
	binary.LittleEndian.PutUint16(buf[vt+2:], uint16(tsize))
	binary.LittleEndian.PutUint32(buf[table:], uint32(int32(table-vt)))
	if far {
		binary.LittleEndian.PutUint16(buf[vt+4+2*k:], 0xfff0)
	} else {
		binary.LittleEndian.PutUint16(buf[vt+4+2*k:], 4)
		binary.LittleEndian.PutUint32(buf[table+4:], 0x7fffff00)
	}
	return buf
}

// TestStructuralVerifierAgreesWithEveryBinding probes every root slot of
// every bound standard twice: once with the field placed outside the buffer
// (a slot the IDL does not know is never checked) and once with the field
// in place holding an out-of-range uoffset (a slot the IDL types as a scalar
// but the binding follows as an offset). Whatever the verifier accepts, the
// binding's accessors must read without a panic.
func TestStructuralVerifierAgreesWithEveryBinding(t *testing.T) {
	const slotCount = 128
	for schema, root := range boundRoots {
		s, err := structureRegistry().Schema(schema)
		if err != nil {
			t.Errorf("%s: no structural verifier: %v", schema, err)
			continue
		}
		ident := s.FileIdentifier()
		dangerous := 0
		for k := 0; k < slotCount; k++ {
			for _, far := range []bool{true, false} {
				buf := slotProbe(ident, slotCount, k, far)
				panicked := accessorsPanic(root, buf) != ""
				if panicked {
					dangerous++
				}
				if VerifyBareStructure(schema, buf) != nil || !panicked {
					continue
				}
				t.Errorf("%s: root slot %d (far=%v) is read by the binding but passes the verifier: the IDL and binding disagree", schema, k, far)
			}
		}
		if dangerous == 0 {
			t.Errorf("%s: no probe reached an accessor; the test proves nothing for it", schema)
		}
	}
}

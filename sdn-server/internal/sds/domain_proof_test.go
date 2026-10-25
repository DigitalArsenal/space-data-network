package sds

import (
	"context"
	"encoding/binary"
	"reflect"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

// retiredDomainSlot is the DomainProof slot SDS 1.264.0 and 1.265.0 stored
// their one domain in. SDS 1.266.0 deprecated it in place, so the generated
// code has no accessor and no adder for it; DOMAINS is slot 7.
const retiredDomainSlot = 0

// epmWithDomainProof is a size-prefixed EPM with one DomainProof. retired fills
// the retired slot the way those releases wrote it; domains fills DOMAINS (nil
// leaves the slot absent).
func epmWithDomainProof(retired string, domains []string) []byte {
	b := flatbuffers.NewBuilder(256)
	var retiredOffset, domainsOffset flatbuffers.UOffsetT
	if retired != "" {
		retiredOffset = b.CreateString(retired)
	}
	if domains != nil {
		names := make([]flatbuffers.UOffsetT, len(domains))
		for i, domain := range domains {
			names[i] = b.CreateString(domain)
		}
		EPM.DomainProofStartDOMAINSVector(b, len(names))
		for i := len(names) - 1; i >= 0; i-- {
			b.PrependUOffsetT(names[i])
		}
		domainsOffset = b.EndVector(len(names))
	}
	publicKey := b.CreateString("02")
	EPM.DomainProofStart(b)
	if retiredOffset != 0 {
		b.PrependUOffsetTSlot(retiredDomainSlot, retiredOffset, 0)
	}
	EPM.DomainProofAddPUBLIC_KEY(b, publicKey)
	if domainsOffset != 0 {
		EPM.DomainProofAddDOMAINS(b, domainsOffset)
	}
	proof := EPM.DomainProofEnd(b)
	EPM.EPMStartDOMAIN_PROOFSVector(b, 1)
	b.PrependUOffsetT(proof)
	proofs := b.EndVector(1)
	dn := b.CreateString("CN=node")
	EPM.EPMStart(b)
	EPM.EPMAddDN(b, dn)
	EPM.EPMAddDOMAIN_PROOFS(b, proofs)
	EPM.FinishSizePrefixedEPMBuffer(b, EPM.EPMEnd(b))
	return append([]byte(nil), b.FinishedBytes()...)
}

func domainProofOf(t *testing.T, data []byte) *EPM.DomainProof {
	t.Helper()
	proof := new(EPM.DomainProof)
	if !EPM.GetSizePrefixedRootAsEPM(data, 0).DOMAIN_PROOFS(proof, 0) {
		t.Fatal("EPM carries no DomainProof")
	}
	return proof
}

func domainsOf(proof *EPM.DomainProof) []string {
	var names []string
	for i := 0; i < proof.DOMAINSLength(); i++ {
		names = append(names, string(proof.DOMAINS(i)))
	}
	return names
}

// The structural verifier walks every offset an accessor can follow, from the
// embedded IDL. A DOMAINS vector or a name that points outside the buffer is
// refused, so the verifier of a domain proof never reads past the record.
func TestDomainProofDOMAINSVectorIsStructurallyVerified(t *testing.T) {
	good := epmWithDomainProof("", []string{"node.example.org", "node.example.com"})
	if err := VerifySizePrefixedStructure("EPM.fbs", good); err != nil {
		t.Fatalf("EPM with a two-name DOMAINS vector refused: %v", err)
	}
	if got := domainsOf(domainProofOf(t, good)); !reflect.DeepEqual(got, []string{"node.example.org", "node.example.com"}) {
		t.Fatalf("DOMAINS = %v", got)
	}

	// DOMAINS is slot 7: the vtable entry at 4+2*7 gives the field's place in
	// the table, and the field holds the forward offset to the vector.
	const domainsVOffset = 4 + 2*7
	tab := domainProofOf(t, good).Table()
	field := tab.Pos + flatbuffers.UOffsetT(tab.Offset(domainsVOffset))
	if field == tab.Pos {
		t.Fatal("DOMAINS slot is absent from the fixture")
	}
	vector := field + flatbuffers.GetUOffsetT(good[field:])

	for name, corrupt := range map[string]func(buf []byte){
		"vector offset past the end": func(buf []byte) { binary.LittleEndian.PutUint32(buf[field:], uint32(len(buf))+64) },
		"name offset past the end":   func(buf []byte) { binary.LittleEndian.PutUint32(buf[vector+4:], uint32(len(buf))+64) },
		"vector length past the end": func(buf []byte) { binary.LittleEndian.PutUint32(buf[vector:], uint32(len(buf))) },
	} {
		t.Run(name, func(t *testing.T) {
			buf := append([]byte(nil), good...)
			corrupt(buf)
			if err := VerifySizePrefixedStructure("EPM.fbs", buf); err == nil {
				t.Fatal("corrupt DOMAINS accepted")
			}
		})
	}
}

// flatc's JSON path still knows the retired DNS_DOMAIN key: it parses it into
// the deprecated slot and prints it back for a stored buffer. The node never
// reads that slot and never maps the key into DOMAINS.
func TestDomainProofRetiredKeyNeverReachesDOMAINS(t *testing.T) {
	v := newConverterValidator(t)
	ctx := context.Background()

	cases := []struct {
		name string
		json string
		want []string
	}{
		{"DOMAINS", `{"DOMAIN_PROOFS":[{"DOMAINS":["a.example","b.example"],"PUBLIC_KEY":"02"}]}`, []string{"a.example", "b.example"}},
		{"retired key alone", `{"DOMAIN_PROOFS":[{"DNS_DOMAIN":"old.example","PUBLIC_KEY":"02"}]}`, nil},
		{"retired key beside DOMAINS", `{"DOMAIN_PROOFS":[{"DNS_DOMAIN":"old.example","DOMAINS":["a.example"],"PUBLIC_KEY":"02"}]}`, []string{"a.example"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := v.JSONToFlatBuffer(ctx, "EPM.fbs", []byte(tc.json), wasm.FlatcSizePrefixed)
			if err != nil {
				t.Fatalf("JSONToFlatBuffer: %v", err)
			}
			if got := domainsOf(domainProofOf(t, data)); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("DOMAINS = %v, want %v", got, tc.want)
			}
		})
	}

	// A buffer written under SDS 1.265.0 holds its one domain in the retired
	// slot and nothing in DOMAINS. It still verifies and decodes; DOMAINS is empty.
	stored := epmWithDomainProof("old.example", nil)
	if err := VerifySizePrefixedStructure("EPM.fbs", stored); err != nil {
		t.Fatalf("1.265.0 buffer refused: %v", err)
	}
	if got := domainsOf(domainProofOf(t, stored)); len(got) != 0 {
		t.Fatalf("DOMAINS of a 1.265.0 buffer = %v, want none", got)
	}
}

package flowrt

import "testing"

// The host's ingress frame descriptor must state "no type claim" EXPLICITLY.
//
// This pins a cross-runtime divergence, not a style preference. TypeDescriptorIdx
// was left at its Go zero value, and 0 is a VALID descriptor index — so the
// guest validated every HTTP request frame against whatever edge happened to be
// first in the compiled table. It passed with the generation-1 flow bundles and
// broke the instant one was recompiled: every mounted flow went inert and
// answered 502 "flow produced no HTTP response", while the module SDK's JS host
// — which has always written FLOW_INVALID_INDEX — served the SAME artifact
// correctly. Alignment 0 is the same defect one field over.
func TestIngressFrameDescriptorMakesNoTypeClaim(t *testing.T) {
	const framePtr = 4096
	const portIDLen = 8
	const payloadLen = 123

	fd := newIngressFrameDescriptor(framePtr, portIDLen, payloadLen)

	if fd.TypeDescriptorIdx != InvalidIndex {
		t.Fatalf(
			"TypeDescriptorIdx = %d, want InvalidIndex (%d): 0 is a real descriptor index, "+
				"so the zero value silently claims edge 0",
			fd.TypeDescriptorIdx, uint32(InvalidIndex),
		)
	}
	if fd.Alignment != 1 {
		t.Fatalf(
			"Alignment = %d, want 1: a $HTQ envelope makes no alignment claim, and the "+
				"JS host defaults this to 1",
			fd.Alignment,
		)
	}
	if !fd.Occupied {
		t.Fatal("Occupied = false: the guest skips an unoccupied ingress slot")
	}
	if fd.Size != payloadLen {
		t.Fatalf("Size = %d, want %d", fd.Size, payloadLen)
	}
	if fd.PortIDPointer != framePtr+flowFrameDescriptorSize {
		t.Fatalf(
			"PortIDPointer = %d, want %d (the NUL-terminated port id follows the descriptor)",
			fd.PortIDPointer, framePtr+flowFrameDescriptorSize,
		)
	}
	if fd.Offset != framePtr+flowFrameDescriptorSize+portIDLen {
		t.Fatalf(
			"Offset = %d, want %d (the payload follows the port id)",
			fd.Offset, framePtr+flowFrameDescriptorSize+portIDLen,
		)
	}
}

// The encoded form is what the guest actually reads, so round-trip it through
// the same encoder/decoder pair the runtime uses.
func TestIngressFrameDescriptorSurvivesEncoding(t *testing.T) {
	fd := newIngressFrameDescriptor(8192, 4, 64)
	buf := make([]byte, flowFrameDescriptorSize)
	encodeFrameDescriptor(buf, fd)

	if got := uint32(buf[4]) | uint32(buf[5])<<8 | uint32(buf[6])<<16 | uint32(buf[7])<<24; got != InvalidIndex {
		t.Fatalf("encoded TypeDescriptorIdx = %#x, want %#x", got, uint32(InvalidIndex))
	}
	if got := uint32(buf[12]) | uint32(buf[13])<<8 | uint32(buf[14])<<16 | uint32(buf[15])<<24; got != 1 {
		t.Fatalf("encoded Alignment = %d, want 1", got)
	}
}

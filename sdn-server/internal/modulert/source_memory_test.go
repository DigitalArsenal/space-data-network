package modulert

import (
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
)

func TestSourceContainerMemoryBudget(t *testing.T) {
	// memory.grow fixture: the guest's 2 GiB declaration cannot override the host cap.
	binary := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00, 0x01, 0x06, 0x01, 0x60, 0x01, 0x7f, 0x01, 0x7f, 0x03, 0x02, 0x01, 0x00, 0x05, 0x06, 0x01, 0x01, 0x01, 0x80, 0x80, 0x02, 0x07, 0x11, 0x02, 0x06, 0x6d, 0x65, 0x6d, 0x6f, 0x72, 0x79, 0x02, 0x00, 0x04, 0x67, 0x72, 0x6f, 0x77, 0x00, 0x00, 0x0a, 0x08, 0x01, 0x06, 0x00, 0x20, 0x00, 0x40, 0x00, 0x0b}
	vm, err := wasmrt.NewModule(binary, wasmrt.WithMaxMemoryPages(defaultModuleMemoryPages))
	if err != nil {
		t.Fatal(err)
	}
	defer vm.Release()
	result, err := vm.Execute("grow", int32(1535))
	if err != nil || len(result) != 1 || result[0] != int32(1) {
		t.Fatalf("96 MiB working set rejected: %v %v", result, err)
	}
	result, err = vm.Execute("grow", int32(2560))
	if err != nil || len(result) != 1 || result[0] != int32(1536) {
		t.Fatalf("bounded growth rejected: %v %v", result, err)
	}
	result, err = vm.Execute("grow", int32(1))
	if err != nil || len(result) != 1 || result[0] != int32(-1) {
		t.Fatalf("host cap exceeded: %v %v", result, err)
	}
}

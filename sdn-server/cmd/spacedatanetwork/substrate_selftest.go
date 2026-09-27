package main

// substrate_selftest.go: `spacedatanetwork substrate-selftest` measures the
// WasmEdge runtime linked into THIS binary and the partition-store host
// pieces (design A30, A31). The release workflow runs it on every native
// build with --require-patched; an operator runs it on a host after a
// rollout. It trusts no version string: upstream 0.16.4 and the patched build
// both call themselves 0.16.4.

import (
	"encoding/json"
	"fmt"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
	"github.com/spf13/cobra"
)

var (
	substrateRequirePatched bool
	substrateSkipAOT        bool
)

var substrateSelftestCmd = &cobra.Command{
	Use:   "substrate-selftest",
	Short: "Measure the linked WasmEdge runtime and the partition-store host I/O module",
	Long: `Run the WasmEdge substrate self-test and print a JSON report:

  atomic_notify_without_store  a notify wakes a waiter whose word never changed
  stop_reaches_every_thread    one executor stop ends every spinning thread
  interruptible_aot            the same for Interruptible THREADS AOT code,
                               which must really run native
  native_host_io               the C host I/O module installs and round-trips

The first three are the SDN WasmEdge patches (scripts/build-static-wasmedge.sh).
With --require-patched the command exits non-zero unless every check passes.`,
	Args: cobra.NoArgs,
	RunE: runSubstrateSelftest,
}

func init() {
	substrateSelftestCmd.Flags().BoolVar(&substrateRequirePatched, "require-patched", false, "exit non-zero unless the runtime is the patched build and every check passes")
	substrateSelftestCmd.Flags().BoolVar(&substrateSkipAOT, "skip-aot", false, "skip the Interruptible AOT check (no LLVM compiler needed)")
	rootCmd.AddCommand(substrateSelftestCmd)
}

type substrateSelftestReport struct {
	wasmrt.SubstrateReport
	Patched         bool   `json:"patched"`
	NativeHostIO    bool   `json:"native_host_io"`
	NativeHostIOErr string `json:"native_host_io_error,omitempty"`
}

func runSubstrateSelftest(cmd *cobra.Command, _ []string) error {
	rep := substrateSelftestReport{SubstrateReport: wasmrt.RunSubstrateSelfTest(wasmrt.SubstrateOptions{SkipAOT: substrateSkipAOT})}
	rep.Patched = rep.SubstrateReport.Patched()
	if err := flatsqlrt.NativeHostIOSelfTest(); err != nil {
		rep.NativeHostIOErr = err.Error()
	} else {
		rep.NativeHostIO = true
	}
	out, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), string(out))
	if substrateRequirePatched && (!rep.Patched || !rep.NativeHostIO || len(rep.Errors) > 0) {
		return fmt.Errorf("substrate self-test failed: patched=%t native_host_io=%t errors=%v", rep.Patched, rep.NativeHostIO, rep.Errors)
	}
	return nil
}

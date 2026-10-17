//go:build darwin

package main

/*
#include <libproc.h>
#include <unistd.h>
static unsigned long long wxf_written(void) {
    struct rusage_info_v4 ri;
    if (proc_pid_rusage(getpid(), RUSAGE_INFO_V4, (rusage_info_t *)&ri) != 0) return 0;
    return ri.ri_diskio_byteswritten;
}
*/
import "C"

func diskWritten() uint64 { return uint64(C.wxf_written()) }

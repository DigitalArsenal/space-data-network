//go:build darwin

package format4proof

/*
#include <libproc.h>
#include <unistd.h>
static int p4proof_diskio(unsigned long long *w, unsigned long long *r) {
	struct rusage_info_v4 ri;
	if (proc_pid_rusage(getpid(), RUSAGE_INFO_V4, (rusage_info_t *)&ri) != 0) return -1;
	*w = ri.ri_diskio_byteswritten;
	*r = ri.ri_diskio_bytesread;
	return 0;
}
*/
import "C"

// diskIO is the bytes this process has written to and read from storage
// devices (proc_pid_rusage), 0 when unavailable.
func diskIO() (written, read uint64) {
	var w, r C.ulonglong
	if C.p4proof_diskio(&w, &r) != 0 {
		return 0, 0
	}
	return uint64(w), uint64(r)
}

//go:build darwin

package update

import "golang.org/x/sys/unix"

// sZomb is p_stat's SZOMB (sys/proc.h): exited, awaiting collection.
const sZomb = 5

// processIsZombie reads the process's state from the kernel (kern.proc.pid).
func processIsZombie(pid int) bool {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	return err == nil && kp.Proc.P_stat == sZomb
}

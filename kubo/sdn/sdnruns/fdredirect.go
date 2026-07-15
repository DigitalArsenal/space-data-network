//go:build unix

package sdnruns

import "syscall"

// syscallDup duplicates a file descriptor, returning the lowest-numbered free
// descriptor. Used to save the process stdin/stdout before redirecting them onto
// a command module's request/response temp files.
func syscallDup(fd int) (int, error) { return syscall.Dup(fd) }

// syscallClose closes a raw file descriptor.
func syscallClose(fd int) error { return syscall.Close(fd) }

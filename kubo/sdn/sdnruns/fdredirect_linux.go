//go:build linux

package sdnruns

import "syscall"

// dupOnto makes newfd refer to the same open file description as oldfd. Linux
// (notably arm64) does not provide dup2, so use dup3 with no flags — equivalent
// to dup2 when oldfd != newfd.
func dupOnto(oldfd, newfd int) error { return syscall.Dup3(oldfd, newfd, 0) }

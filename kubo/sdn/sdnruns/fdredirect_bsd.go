//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package sdnruns

import "syscall"

// dupOnto makes newfd refer to the same open file description as oldfd
// (the classic dup2 semantics), used to wire a command module's stdin/stdout to
// host temp files around its _start run.
func dupOnto(oldfd, newfd int) error { return syscall.Dup2(oldfd, newfd) }

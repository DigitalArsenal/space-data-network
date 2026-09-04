//go:build !windows

package flatsqldrv

import "syscall"

func setUmask(mask int) int { return syscall.Umask(mask) }

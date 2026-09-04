//go:build windows

package flatsqldrv

func setUmask(mask int) int { return mask }

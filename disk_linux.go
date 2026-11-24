//go:build linux

package main

import "syscall"

// statfs reads a mount's size like df: used is blocks not free, total is used plus what's available to users (so
// the percentage matches df's Use%).
func statfs(path string) (Disk, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil || st.Blocks == 0 {
		return Disk{}, false
	}
	bsize := uint64(st.Bsize)
	used := (st.Blocks - st.Bfree) * bsize
	d := Disk{Mount: path, Used: used, Total: used + st.Bavail*bsize}
	if st.Files > 0 {
		iu, it := st.Files-st.Ffree, st.Files
		d.InodesUsed, d.InodesTotal = &iu, &it
	}
	return d, true
}

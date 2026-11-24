//go:build !linux

package main

// The agent targets Linux; elsewhere (building and testing on a Mac) disks are left out.
func statfs(string) (Disk, bool) { return Disk{}, false }

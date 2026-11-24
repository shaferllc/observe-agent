package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCPUPercentFromTwoReadings(t *testing.T) {
	a, ok := parseCPUStat(strings.NewReader("cpu  100 0 100 700 100 0 0 0 0 0\ncpu0 1 2 3 4\n"))
	if !ok || a.total != 1000 || a.idle != 800 {
		t.Fatalf("first reading = %+v %v", a, ok)
	}
	b, _ := parseCPUStat(strings.NewReader("cpu  200 0 200 1500 100 0 0 0 0 0\n"))
	if got := *cpuPercent(a, b); got != 20 {
		t.Fatalf("cpu = %v, want 20", got)
	}
	if cpuPercent(b, a) != nil {
		t.Fatal("counters going backwards (a reboot) must give no reading")
	}
}

func TestLoadavg(t *testing.T) {
	got := parseLoadavg(strings.NewReader("0.52 0.41 0.30 1/234 5678\n"))
	if len(got) != 3 || got[0] != 0.52 || got[2] != 0.30 {
		t.Fatalf("load = %v", got)
	}
}

func TestMeminfoUsesAvailable(t *testing.T) {
	mem, swap := parseMeminfo(strings.NewReader("MemTotal:  1000 kB\nMemFree: 100 kB\nMemAvailable: 400 kB\nSwapTotal: 200 kB\nSwapFree: 150 kB\n"))
	if mem.Total != 1000*1024 || mem.Used != 600*1024 {
		t.Fatalf("mem = %+v", mem)
	}
	if swap.Total != 200*1024 || swap.Used != 50*1024 {
		t.Fatalf("swap = %+v", swap)
	}
}

func TestNetDevSkipsLoopbackAndGivesRates(t *testing.T) {
	dev := `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 999999 0 0 0 0 0 0 0 999999 0 0 0 0 0 0 0
  eth0: 1000 0 0 0 0 0 0 0 2000 0 0 0 0 0 0 0
  eth1: 500 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0
`
	a, ok := parseNetDev(strings.NewReader(dev))
	if !ok || a.rx != 1500 || a.tx != 2000 {
		t.Fatalf("net = %+v", a)
	}
	r := netRates(a, netBytes{rx: 7500, tx: 8000}, 60)
	if *r.RxBps != 100 || *r.TxBps != 100 {
		t.Fatalf("rates = %d %d", *r.RxBps, *r.TxBps)
	}
	if netRates(netBytes{rx: 10}, netBytes{rx: 5}, 60) != nil {
		t.Fatal("a counter reset must give no rate")
	}
}

func TestMountsKeepRealFilesystemsOncePerDevice(t *testing.T) {
	got := parseMounts(strings.NewReader(`/dev/sda1 / ext4 rw 0 0
proc /proc proc rw 0 0
tmpfs /run tmpfs rw 0 0
/dev/sdb1 /mnt/my\040data xfs rw 0 0
/dev/sda1 /var/lib/docker/bind ext4 rw 0 0
overlay /var/lib/docker/overlay2/x/merged overlay rw 0 0
server:/export /mnt/nfs nfs4 rw 0 0
/dev/sdc1 /var/lib/kubelet/pods/abc/volumes/x ext4 rw 0 0
/dev/loop3 /snap/core/1 squashfs ro 0 0
`))
	if strings.Join(got, "|") != "/|/mnt/my data" {
		t.Fatalf("mounts = %q", got)
	}
}

func TestUptime(t *testing.T) {
	if got := parseUptime(strings.NewReader("12345.67 54321.00\n")); got == nil || *got != 12345 {
		t.Fatalf("uptime = %v", got)
	}
}

func TestRunningProcessesMatchesComm(t *testing.T) {
	proc := t.TempDir()
	for pid, comm := range map[string]string{"1": "systemd", "42": "nginx", "77": "php-fpm8.3-maste"[:15]} {
		os.MkdirAll(filepath.Join(proc, pid), 0o755)
		os.WriteFile(filepath.Join(proc, pid, "comm"), []byte(comm+"\n"), 0o644)
	}
	got := runningProcesses(proc, []string{"nginx", "redis-server", "php-fpm8.3-master"})
	if !got["nginx"] || got["redis-server"] || !got["php-fpm8.3-master"] {
		t.Fatalf("processes = %v", got)
	}
}

func TestCollectFirstSampleHasNoRates(t *testing.T) {
	proc := t.TempDir()
	os.WriteFile(filepath.Join(proc, "stat"), []byte("cpu 1 0 1 8 0 0 0 0\n"), 0o644)
	os.WriteFile(filepath.Join(proc, "loadavg"), []byte("1 2 3 1/1 1\n"), 0o644)
	c := &Collector{Proc: proc, statfs: func(string) (Disk, bool) { return Disk{}, false }, mountsFile: filepath.Join(proc, "none")}
	s := c.Collect(time.Unix(100, 0), nil)
	if s.TS != 100 || s.CPUPct != nil || len(s.Load) != 3 {
		t.Fatalf("first sample = %+v", s)
	}
	os.WriteFile(filepath.Join(proc, "stat"), []byte("cpu 2 0 2 16 0 0 0 0\n"), 0o644)
	if s := c.Collect(time.Unix(160, 0), nil); s.CPUPct == nil || *s.CPUPct != 20 {
		t.Fatalf("second sample cpu = %v", s.CPUPct)
	}
}

func TestCollectKeepsTheLargestDisksWhenThereAreTooMany(t *testing.T) {
	proc := t.TempDir()
	var mounts strings.Builder
	for i := 0; i < 40; i++ {
		mounts.WriteString("pool/ds" + strconv.Itoa(i) + " /tank/ds" + strconv.Itoa(i) + " zfs rw 0 0\n")
	}
	mountsFile := filepath.Join(proc, "mounts")
	os.WriteFile(mountsFile, []byte(mounts.String()), 0o644)
	c := &Collector{Proc: proc, mountsFile: mountsFile, statfs: func(p string) (Disk, bool) {
		n, _ := strconv.Atoi(strings.TrimPrefix(p, "/tank/ds"))
		return Disk{Mount: p, Total: uint64(n + 1)}, true
	}}
	s := c.Collect(time.Unix(1, 0), nil)
	if len(s.Disks) != MaxDisks || s.Disks[0].Mount != "/tank/ds39" || s.Disks[MaxDisks-1].Mount != "/tank/ds8" {
		t.Fatalf("disks = %d, first %s, last %s", len(s.Disks), s.Disks[0].Mount, s.Disks[len(s.Disks)-1].Mount)
	}
}

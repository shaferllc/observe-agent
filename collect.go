package main

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Sample is one minute's reading, in the shape Observe's /api/agent/v1/metrics takes.
type Sample struct {
	TS        int64           `json:"ts"`
	CPUPct    *float64        `json:"cpu_pct"`
	Load      []float64       `json:"load,omitempty"`
	Memory    *Usage          `json:"memory,omitempty"`
	Swap      *Usage          `json:"swap,omitempty"`
	Disks     []Disk          `json:"disks,omitempty"`
	Net       *Net            `json:"net,omitempty"`
	UptimeS   *int64          `json:"uptime_s,omitempty"`
	Processes map[string]bool `json:"processes,omitempty"`
}

type Usage struct {
	Used  uint64 `json:"used"`
	Total uint64 `json:"total"`
}

type Disk struct {
	Mount       string  `json:"mount"`
	Used        uint64  `json:"used"`
	Total       uint64  `json:"total"`
	InodesUsed  *uint64 `json:"inodes_used,omitempty"`
	InodesTotal *uint64 `json:"inodes_total,omitempty"`
}

type Net struct {
	RxBps *uint64 `json:"rx_bps"`
	TxBps *uint64 `json:"tx_bps"`
}

// Collector reads /proc. CPU and network are rates, so it keeps the previous counters; the first reading (and one
// after a counter goes backwards, as after a reboot) has none.
type Collector struct {
	Proc string // /proc, or a fixture directory in tests

	prevCPU    *cpuTimes
	prevNet    *netBytes
	prevNetAt  time.Time
	statfs     func(path string) (Disk, bool)
	mountsFile string
}

func NewCollector() *Collector {
	return &Collector{Proc: "/proc", statfs: statfs, mountsFile: "/proc/mounts"}
}

func (c *Collector) open(name string) (io.ReadCloser, error) {
	return os.Open(filepath.Join(c.Proc, name))
}

// Collect reads everything once. Parts that can't be read are left out rather than failing the sample.
func (c *Collector) Collect(now time.Time, processes []string) Sample {
	s := Sample{TS: now.Unix()}

	if f, err := c.open("stat"); err == nil {
		if cur, ok := parseCPUStat(f); ok {
			if c.prevCPU != nil {
				s.CPUPct = cpuPercent(*c.prevCPU, cur)
			}
			c.prevCPU = &cur
		}
		f.Close()
	}
	if f, err := c.open("loadavg"); err == nil {
		s.Load = parseLoadavg(f)
		f.Close()
	}
	if f, err := c.open("meminfo"); err == nil {
		s.Memory, s.Swap = parseMeminfo(f)
		f.Close()
	}
	if f, err := c.open("net/dev"); err == nil {
		if cur, ok := parseNetDev(f); ok {
			if c.prevNet != nil {
				s.Net = netRates(*c.prevNet, cur, now.Sub(c.prevNetAt).Seconds())
			}
			c.prevNet, c.prevNetAt = &cur, now
		}
		f.Close()
	}
	if f, err := c.open("uptime"); err == nil {
		s.UptimeS = parseUptime(f)
		f.Close()
	}
	if f, err := os.Open(c.mountsFile); err == nil {
		for _, m := range parseMounts(f) {
			if d, ok := c.statfs(m); ok {
				s.Disks = append(s.Disks, d)
			}
		}
		f.Close()
		s.Disks = largestDisks(s.Disks)
	}
	if len(processes) > 0 {
		s.Processes = runningProcesses(c.Proc, processes)
	}

	return s
}

type cpuTimes struct{ total, idle uint64 }

// parseCPUStat reads the aggregate "cpu" line: user nice system idle iowait irq softirq steal (guest is already
// counted in user).
func parseCPUStat(r io.Reader) (cpuTimes, bool) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 5 || f[0] != "cpu" {
			continue
		}
		var t cpuTimes
		for i := 1; i < len(f) && i <= 8; i++ {
			v, err := strconv.ParseUint(f[i], 10, 64)
			if err != nil {
				return cpuTimes{}, false
			}
			t.total += v
			if i == 4 || i == 5 { // idle, iowait
				t.idle += v
			}
		}
		return t, true
	}
	return cpuTimes{}, false
}

func cpuPercent(prev, cur cpuTimes) *float64 {
	if cur.total <= prev.total || cur.idle < prev.idle {
		return nil
	}
	dt, di := float64(cur.total-prev.total), float64(cur.idle-prev.idle)
	pct := 100 * (dt - di) / dt
	if pct < 0 {
		pct = 0
	}
	return &pct
}

func parseLoadavg(r io.Reader) []float64 {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil
	}
	f := strings.Fields(string(b))
	if len(f) < 3 {
		return nil
	}
	out := make([]float64, 3)
	for i := 0; i < 3; i++ {
		v, err := strconv.ParseFloat(f[i], 64)
		if err != nil {
			return nil
		}
		out[i] = v
	}
	return out
}

// parseMeminfo returns used memory as total minus MemAvailable (what free(1) calls "available"), and swap.
func parseMeminfo(r io.Reader) (*Usage, *Usage) {
	kb := map[string]uint64{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 {
			continue
		}
		if v, err := strconv.ParseUint(f[1], 10, 64); err == nil {
			kb[strings.TrimSuffix(f[0], ":")] = v * 1024
		}
	}
	var mem, swap *Usage
	if total, ok := kb["MemTotal"]; ok && total > 0 {
		avail, ok := kb["MemAvailable"]
		if !ok {
			avail = kb["MemFree"] + kb["Buffers"] + kb["Cached"]
		}
		if avail > total {
			avail = total
		}
		mem = &Usage{Used: total - avail, Total: total}
	}
	if total, ok := kb["SwapTotal"]; ok {
		free := kb["SwapFree"]
		if free > total {
			free = total
		}
		swap = &Usage{Used: total - free, Total: total}
	}
	return mem, swap
}

type netBytes struct{ rx, tx uint64 }

// parseNetDev sums received and sent bytes over every interface but loopback.
func parseNetDev(r io.Reader) (netBytes, bool) {
	var n netBytes
	found := false
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		if strings.TrimSpace(name) == "lo" {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			continue
		}
		rx, err1 := strconv.ParseUint(f[0], 10, 64)
		tx, err2 := strconv.ParseUint(f[8], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		n.rx += rx
		n.tx += tx
		found = true
	}
	return n, found
}

func netRates(prev, cur netBytes, seconds float64) *Net {
	if seconds <= 0 || cur.rx < prev.rx || cur.tx < prev.tx {
		return nil
	}
	rx := uint64(float64(cur.rx-prev.rx) / seconds)
	tx := uint64(float64(cur.tx-prev.tx) / seconds)
	return &Net{RxBps: &rx, TxBps: &tx}
}

func parseUptime(r io.Reader) *int64 {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return nil
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return nil
	}
	s := int64(v)
	return &s
}

// Filesystems that aren't disks (memory, kernel interfaces, container layers, read-only images).
var pseudoFS = map[string]bool{
	"proc": true, "sysfs": true, "tmpfs": true, "devtmpfs": true, "devpts": true, "cgroup": true, "cgroup2": true,
	"overlay": true, "squashfs": true, "securityfs": true, "pstore": true, "debugfs": true, "tracefs": true,
	"mqueue": true, "hugetlbfs": true, "fusectl": true, "configfs": true, "bpf": true, "autofs": true,
	"binfmt_misc": true, "nsfs": true, "rpc_pipefs": true, "ramfs": true, "efivarfs": true, "fuse.lxcfs": true,
	"selinuxfs": true, "nfsd": true, "fuse.snapfuse": true, "iso9660": true,
	// Network filesystems: statfs on a hung one blocks, which would stall every sample.
	"nfs": true, "nfs4": true, "cifs": true, "smb3": true, "smbfs": true, "fuse.sshfs": true, "ceph": true,
	"glusterfs": true, "fuse.glusterfs": true, "9p": true, "afs": true,
}

// Container and snap mount points: one per layer, volume or pod, and not the host's own disks.
var skippedMountPrefixes = []string{"/var/lib/docker/", "/var/lib/kubelet/", "/var/lib/containers/", "/run/containerd/", "/snap/"}

// MaxDisks matches the most mounts Observe takes per sample.
const MaxDisks = 32

// parseMounts lists the mount points of real filesystems, once per device (bind mounts repeat one).
func parseMounts(r io.Reader) []string {
	var out []string
	seen := map[string]bool{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 3 || pseudoFS[f[2]] || seen[f[0]] {
			continue
		}
		mount := unescapeMount(f[1])
		if hasAnyPrefix(mount, skippedMountPrefixes) {
			continue
		}
		seen[f[0]] = true
		out = append(out, mount)
	}
	return out
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// largestDisks keeps the MaxDisks biggest filesystems (a ZFS host has a mount per dataset).
func largestDisks(disks []Disk) []Disk {
	if len(disks) <= MaxDisks {
		return disks
	}
	sort.SliceStable(disks, func(i, j int) bool { return disks[i].Total > disks[j].Total })
	return disks[:MaxDisks]
}

// unescapeMount decodes /proc/mounts' octal escapes (\040 is a space).
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// runningProcesses says, for each name, whether a process with that name (its comm, which the kernel cuts to 15
// characters) is running.
func runningProcesses(proc string, names []string) map[string]bool {
	running := map[string]bool{}
	entries, _ := os.ReadDir(proc)
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join(proc, e.Name(), "comm"))
		if err == nil {
			running[strings.TrimSpace(string(b))] = true
		}
	}
	out := make(map[string]bool, len(names))
	for _, n := range names {
		comm := n
		if len(comm) > 15 {
			comm = comm[:15]
		}
		out[n] = running[comm]
	}
	return out
}

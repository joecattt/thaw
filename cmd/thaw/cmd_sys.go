package main

import (
	"bufio"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// printSystemSnapshot is the "real time CPU/memory/disk, where it's all
// being allocated" complication — live macOS reads (ps/vm_stat/df/sysctl),
// no thaw-owned data involved. Read-only throughout: this shows what's
// using space and cycles, it never acts on it.
func printSystemSnapshot() {
	load, ncpu := loadAvg()
	memUsedGB, memTotalGB := memSnapshot()
	disk := diskSnapshot("/System/Volumes/Data")

	fmt.Printf("\n  🖥 load %.1f/%.1f/%.1f (%d cores)   🧠 %.1fG/%.1fG used   💾 %s\n",
		load[0], load[1], load[2], ncpu, memUsedGB, memTotalGB, disk)

	if procs := topProcesses(3); len(procs) > 0 {
		fmt.Println("  top cpu:")
		for _, p := range procs {
			fmt.Printf("    %5.1f%% cpu  %5.1f%% mem  %s\n", p.cpu, p.mem, p.name)
		}
	}
}

func loadAvg() ([3]float64, int) {
	var out [3]float64
	raw, err := exec.Command("sysctl", "-n", "vm.loadavg").Output()
	if err == nil {
		s := strings.Trim(strings.TrimSpace(string(raw)), "{ }")
		fields := strings.Fields(s)
		for i := 0; i < 3 && i < len(fields); i++ {
			out[i], _ = strconv.ParseFloat(fields[i], 64)
		}
	}
	ncpu := 0
	if raw, err := exec.Command("sysctl", "-n", "hw.ncpu").Output(); err == nil {
		ncpu, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
	}
	return out, ncpu
}

// memSnapshot reads vm_stat (page counts) + hw.memsize (bytes) and returns
// used/total in GB. "Used" = active+inactive+wired, matching Activity
// Monitor's rough definition — free+speculative counts as available.
func memSnapshot() (usedGB, totalGB float64) {
	totalBytes := 0.0
	if raw, err := exec.Command("sysctl", "-n", "hw.memsize").Output(); err == nil {
		if v, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64); err == nil {
			totalBytes = float64(v)
		}
	}
	out, err := exec.Command("vm_stat").Output()
	if err != nil || totalBytes == 0 {
		return 0, totalBytes / (1 << 30)
	}
	pageSize := 4096.0
	vals := map[string]float64{}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		line := sc.Text()
		if !strings.Contains(line, ":") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		key := strings.TrimSpace(parts[0])
		numStr := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(parts[1]), "."))
		if n, err := strconv.ParseFloat(numStr, 64); err == nil {
			vals[key] = n
		}
	}
	used := (vals["Pages active"] + vals["Pages inactive"] + vals["Pages wired down"]) * pageSize
	return used / (1 << 30), totalBytes / (1 << 30)
}

// diskSnapshot reports used/available on the given mount (macOS's real data
// volume is /System/Volumes/Data, not / — the boot volume is a thin
// read-only system snapshot and always reports nearly full).
func diskSnapshot(mount string) string {
	out, err := exec.Command("df", "-H", mount).Output()
	if err != nil {
		return "unavailable"
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return "unavailable"
	}
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 5 {
		return "unavailable"
	}
	return fmt.Sprintf("%s used / %s free (%s)", fields[2], fields[3], fields[4])
}

type procStat struct {
	cpu, mem float64
	name     string
}

// topProcesses is the "where's it all being allocated" answer — real ps
// output, sorted by CPU, trimmed to a process name a human would recognize
// (last path component, not the full framework path).
func topProcesses(n int) []procStat {
	out, err := exec.Command("ps", "-A", "-o", "pcpu,pmem,comm", "-r").Output()
	if err != nil {
		return nil
	}
	var procs []procStat
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	first := true
	for sc.Scan() {
		if first {
			first = false // header row
			continue
		}
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 {
			continue
		}
		cpu, _ := strconv.ParseFloat(fields[0], 64)
		mem, _ := strconv.ParseFloat(fields[1], 64)
		name := fields[len(fields)-1]
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		procs = append(procs, procStat{cpu, mem, name})
	}
	sort.Slice(procs, func(i, j int) bool { return procs[i].cpu > procs[j].cpu })
	if len(procs) > n {
		procs = procs[:n]
	}
	return procs
}

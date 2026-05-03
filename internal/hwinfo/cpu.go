package hwinfo

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/mem"
)

// cpuPercentFirstCall guards the first cpu.Percent invocation: with
// interval=0 the first call returns 0 because there's no prior
// snapshot to delta against. We do an initial throwaway call once at
// import-time so the first user-facing Snapshot has a real number.
var cpuPercentFirstCall sync.Once

func cpuSnapshot() []Device {
	cpuPercentFirstCall.Do(func() {
		_, _ = cpu.Percent(0, false)
	})

	infos, err := cpu.Info()
	if err != nil || len(infos) == 0 {
		return nil
	}
	// Dedupe by PhysicalID so one row per socket, not per logical
	// core. PhysicalID is empty on some kernels — fall back to the
	// first entry as a single-socket assumption then.
	seen := map[string]bool{}
	var sockets []cpu.InfoStat
	for _, info := range infos {
		id := info.PhysicalID
		if id == "" {
			id = "0"
		}
		if !seen[id] {
			seen[id] = true
			sockets = append(sockets, info)
		}
	}

	utilPcts, _ := cpu.Percent(0, false)
	utilOverall := 0
	if len(utilPcts) > 0 {
		utilOverall = int(utilPcts[0] + 0.5)
	}
	memPct := 0
	var memUsed, memTotal uint64
	if vm, err := mem.VirtualMemory(); err == nil {
		memPct = int(vm.UsedPercent + 0.5)
		memUsed = vm.Used
		memTotal = vm.Total
	}

	temps, tempMax := readCPUTemps()
	powerW, hasPower := readCPUPower()
	powerMaxW := readCPUPowerMax()
	fanRPM, hasFan := readFirstFan()

	out := make([]Device, 0, len(sockets))
	for i, info := range sockets {
		d := Device{
			Class:         ClassCPU,
			Index:         i,
			Name:          cleanupCPUName(info.ModelName),
			UtilPct:       utilOverall,
			MemPct:        memPct,
			MemUsedBytes:  memUsed,
			MemTotalBytes: memTotal,
		}
		// Per-socket temp if we found one; falls back to package temp.
		if t, ok := temps[i]; ok {
			d.TempC = t
			d.HasTemp = true
		} else if t, ok := temps[0]; ok && len(sockets) == 1 {
			d.TempC = t
			d.HasTemp = true
		}
		if d.HasTemp {
			d.TempMaxC = tempMax
		}
		// Power/fan are system-wide; attach only to socket 0 to avoid
		// double-counting in multi-socket displays.
		if i == 0 && hasPower {
			d.PowerW = powerW
			d.PowerMaxW = powerMaxW
			d.HasPower = true
		}
		if i == 0 && hasFan {
			d.FanRPM = fanRPM
			d.HasFan = true
		}
		out = append(out, d)
	}
	return out
}

// cleanupCPUName trims the "Intel(R) Core(TM) " / multiple-spaces noise
// that vendor strings ship with so the panel reads cleanly. Best-effort
// — the goal is shorter, not canonical.
func cleanupCPUName(name string) string {
	for _, sub := range []string{"(R)", "(TM)", "CPU @ "} {
		name = strings.ReplaceAll(name, sub, "")
	}
	for strings.Contains(name, "  ") {
		name = strings.ReplaceAll(name, "  ", " ")
	}
	return strings.TrimSpace(name)
}

// readCPUTemps walks gopsutil's sensor list looking for the canonical
// per-socket package temp keys. Returns a map of socket index → °C
// plus the throttle ceiling (Tjmax / Critical, 0 if not exposed). We
// tolerate any of the common SensorKey patterns: coretemp's
// "Package id N", k10temp/zenpower's "Tctl"/"Tdie", or plain "package".
func readCPUTemps() (map[int]int, int) {
	out := map[int]int{}
	tempMax := 0
	sensors, err := host.SensorsTemperatures()
	if err != nil {
		return out, 0
	}
	for _, s := range sensors {
		key := strings.ToLower(s.SensorKey)
		switch {
		case strings.HasPrefix(key, "package id "):
			idx, _ := strconv.Atoi(strings.TrimPrefix(key, "package id "))
			out[idx] = int(s.Temperature + 0.5)
			if s.Critical > 0 && tempMax == 0 {
				tempMax = int(s.Critical + 0.5)
			}
		case key == "tctl" || key == "tdie" || key == "k10temp":
			if _, ok := out[0]; !ok {
				out[0] = int(s.Temperature + 0.5)
				if s.Critical > 0 && tempMax == 0 {
					tempMax = int(s.Critical + 0.5)
				}
			}
		}
	}
	// Fallback: first sensor whose key looks like a CPU temp.
	if len(out) == 0 {
		for _, s := range sensors {
			key := strings.ToLower(s.SensorKey)
			if strings.Contains(key, "core") || strings.Contains(key, "cpu") {
				out[0] = int(s.Temperature + 0.5)
				if s.Critical > 0 && tempMax == 0 {
					tempMax = int(s.Critical + 0.5)
				}
				break
			}
		}
	}
	return out, tempMax
}

// readCPUPowerMax returns the configured long-term TDP from RAPL
// constraint_0_max_power_uw (microwatts → watts). Returns 0 when the
// path is missing or the value is zero/invalid (e.g. older kernels
// or virtualized hosts where powercap is absent).
func readCPUPowerMax() int {
	const path = "/sys/class/powercap/intel-rapl:0/constraint_0_max_power_uw"
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil || v == 0 {
		return 0
	}
	return int(v / 1_000_000)
}

// rapEnergySnap captures the (energy_uj, time) pair from the
// previous power read so the next read can compute average watts
// across the interval. Per-process state — Snapshot is called from a
// single goroutine.
var (
	rapEnergyMu   sync.Mutex
	rapEnergyPrev uint64
	rapEnergyTime time.Time
)

// readCPUPower averages CPU package power across the gap between two
// consecutive reads of /sys/class/powercap/intel-rapl:0/energy_uj.
// Returns (watts, true) once a baseline has been established and at
// least 100ms has elapsed; first read returns (0, false) so the panel
// shows n/a until the second tick.
//
// Best-effort: AMD Zen exposes the same hierarchy; missing path on
// older kernels = silent n/a.
func readCPUPower() (int, bool) {
	const path = "/sys/class/powercap/intel-rapl:0/energy_uj"
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	current, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return 0, false
	}
	now := time.Now()

	rapEnergyMu.Lock()
	prev := rapEnergyPrev
	prevTime := rapEnergyTime
	rapEnergyPrev = current
	rapEnergyTime = now
	rapEnergyMu.Unlock()

	if prev == 0 || prevTime.IsZero() {
		return 0, false
	}
	dt := now.Sub(prevTime).Seconds()
	if dt < 0.1 {
		return 0, false
	}
	// Energy counter wraps; if current < prev, treat as one wrap.
	var dEnergyUJ uint64
	if current >= prev {
		dEnergyUJ = current - prev
	} else {
		dEnergyUJ = current
	}
	watts := float64(dEnergyUJ) / 1_000_000.0 / dt
	if watts < 0 || watts > 1000 {
		return 0, false
	}
	return int(watts + 0.5), true
}

// readFirstFan walks /sys/class/hwmon for the first fan*_input that
// reports a non-zero RPM. Best-effort; many systems → false.
func readFirstFan() (int, bool) {
	matches, err := filepath.Glob("/sys/class/hwmon/hwmon*/fan*_input")
	if err != nil {
		return 0, false
	}
	for _, p := range matches {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		v, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil || v <= 0 {
			continue
		}
		return v, true
	}
	return 0, false
}

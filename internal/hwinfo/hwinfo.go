// Package hwinfo collects per-device hardware utilization for the
// run-mode header's Hardware panel. CPU info comes from gopsutil
// (pure Go, always available). GPU info comes from NVIDIA's NVML
// (CGO; the binary still runs on non-NVIDIA hosts because nvml.Init
// fails fast and Snapshot returns just the CPU rows).
package hwinfo

// Class is the device-class enum emitted by Snapshot: CPU sockets
// come first, then NVIDIA GPUs. Indexing is per-class so the user
// sees `[0]`, `[1]`... within each block rather than a global counter.
type Class int

const (
	ClassCPU Class = iota
	ClassGPU
)

// Device is one row in the Hardware panel. The Has* booleans signal
// whether the corresponding numeric field is meaningful — many
// systems don't expose power or fan, so we render `n/a` cleanly
// instead of misleading zeros.
type Device struct {
	Class    Class
	Index    int
	Name     string
	UtilPct  int // 0–100
	MemPct   int // 0–100
	PowerW   int
	TempC    int
	FanRPM   int
	FanPct   int
	HasPower bool
	HasTemp  bool
	HasFan   bool
}

// Snapshot returns the current state of every available CPU socket
// and GPU. CPU sockets always come first, then GPUs. All values are
// "best effort": missing data manifests as a zeroed field with the
// matching Has* flag false, never an aborted snapshot — the panel
// must keep rendering through hardware quirks.
func Snapshot() ([]Device, error) {
	out := cpuSnapshot()
	out = append(out, gpuSnapshot()...)
	return out, nil
}

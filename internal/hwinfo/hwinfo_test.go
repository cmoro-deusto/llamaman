package hwinfo

import (
	"runtime"
	"testing"
)

// TestSnapshotReturnsAtLeastOneCPU pins the minimum contract: every
// supported host produces at least one CPU socket entry. CI machines
// may not have GPUs but they always have a CPU.
func TestSnapshotReturnsAtLeastOneCPU(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("hwinfo is Linux-only for now")
	}
	devs, err := Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	cpuCount := 0
	for _, d := range devs {
		if d.Class == ClassCPU {
			cpuCount++
		}
	}
	if cpuCount < 1 {
		t.Errorf("expected at least 1 CPU device, got %d (full devs: %+v)", cpuCount, devs)
	}
}

// TestSnapshotCPUFieldsBestEffort confirms that missing optional
// fields surface as Has*=false rather than nuking the snapshot.
// Power and fan are commonly missing on minimal/VM hosts and that
// must not break rendering.
func TestSnapshotCPUFieldsBestEffort(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("hwinfo is Linux-only for now")
	}
	devs, err := Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	for _, d := range devs {
		if d.Class != ClassCPU {
			continue
		}
		// Util/MemPct are always sourced from gopsutil; even on a quiet
		// system they produce a value (0% is valid). The Has* flags
		// gate the optional rapsl/sensors slots — just exercising
		// that we don't panic and the values stay in range.
		if d.UtilPct < 0 || d.UtilPct > 100 {
			t.Errorf("CPU UtilPct out of range: %d", d.UtilPct)
		}
		if d.MemPct < 0 || d.MemPct > 100 {
			t.Errorf("CPU MemPct out of range: %d", d.MemPct)
		}
	}
}

// TestSnapshotGPUEmptyOnNonNvidia pins the contract that NVML init
// failure (no NVIDIA driver, no libnvidia-ml.so) yields zero GPU
// entries rather than an error — the binary must run on hosts
// without an NVIDIA card.
func TestSnapshotGPUEmptyOnNonNvidia(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("hwinfo is Linux-only for now")
	}
	if nvmlAvailable {
		t.Skip("NVIDIA NVML available on this host; this test only meaningful when it isn't")
	}
	devs, err := Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	for _, d := range devs {
		if d.Class == ClassGPU {
			t.Errorf("expected no GPU devices when NVML unavailable; got %+v", d)
		}
	}
}

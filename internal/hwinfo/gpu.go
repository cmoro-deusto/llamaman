package hwinfo

import (
	"sync"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
)

// nvmlState lazily initializes the NVML library on the first call to
// gpuSnapshot. nvml.Init() does an internal dlopen of libnvidia-ml.so;
// when the library is missing or fails to load we cache that failure
// so subsequent ticks don't re-attempt and pay the syscall cost.
var (
	nvmlOnce      sync.Once
	nvmlAvailable bool
)

func gpuSnapshot() []Device {
	nvmlOnce.Do(func() {
		ret := nvml.Init()
		nvmlAvailable = ret == nvml.SUCCESS
	})
	if !nvmlAvailable {
		return nil
	}
	count, ret := nvml.DeviceGetCount()
	if ret != nvml.SUCCESS {
		return nil
	}
	out := make([]Device, 0, count)
	for i := 0; i < count; i++ {
		dev, ret := nvml.DeviceGetHandleByIndex(i)
		if ret != nvml.SUCCESS {
			continue
		}
		d := Device{Class: ClassGPU, Index: i}
		if name, ret := dev.GetName(); ret == nvml.SUCCESS {
			d.Name = name
		} else {
			d.Name = "(NVIDIA GPU)"
		}
		if util, ret := dev.GetUtilizationRates(); ret == nvml.SUCCESS {
			d.UtilPct = int(util.Gpu)
		}
		if memInfo, ret := dev.GetMemoryInfo(); ret == nvml.SUCCESS && memInfo.Total > 0 {
			d.MemPct = int((memInfo.Used * 100) / memInfo.Total)
			d.MemUsedBytes = memInfo.Used
			d.MemTotalBytes = memInfo.Total
		}
		if mw, ret := dev.GetPowerUsage(); ret == nvml.SUCCESS {
			d.PowerW = int((mw + 500) / 1000)
			d.HasPower = true
		}
		if mw, ret := dev.GetPowerManagementLimit(); ret == nvml.SUCCESS {
			d.PowerMaxW = int((mw + 500) / 1000)
		}
		if t, ret := dev.GetTemperature(nvml.TEMPERATURE_GPU); ret == nvml.SUCCESS {
			d.TempC = int(t)
			d.HasTemp = true
		}
		// Throttle ceiling: prefer SLOWDOWN (where perf clock drops),
		// fall back to SHUTDOWN. The user-facing meaning of "near
		// throttle" is the slowdown threshold.
		if max, ret := dev.GetTemperatureThreshold(nvml.TEMPERATURE_THRESHOLD_SLOWDOWN); ret == nvml.SUCCESS {
			d.TempMaxC = int(max)
		} else if max, ret := dev.GetTemperatureThreshold(nvml.TEMPERATURE_THRESHOLD_SHUTDOWN); ret == nvml.SUCCESS {
			d.TempMaxC = int(max)
		}
		if pct, ret := dev.GetFanSpeed(); ret == nvml.SUCCESS {
			d.FanPct = int(pct)
			d.HasFan = true
		}
		out = append(out, d)
	}
	return out
}

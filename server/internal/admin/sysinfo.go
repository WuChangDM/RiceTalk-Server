package admin

import (
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/mem"
	"github.com/shirou/gopsutil/v3/net"
)

func getCPUUsage() float64 {
	percentages, err := cpu.Percent(0, false)
	if err != nil || len(percentages) == 0 {
		return 0
	}
	return percentages[0]
}

// getSystemMemory returns system memory stats (total/used/available/percent).
// Returns nil if the information is unavailable.
func getSystemMemory() *mem.VirtualMemoryStat {
	m, err := mem.VirtualMemory()
	if err != nil {
		return nil
	}
	return m
}

// getNetworkIO returns aggregate network IO counters (bytes received/sent).
// Returns nil if the information is unavailable.
func getNetworkIO() *net.IOCountersStat {
	stats, err := net.IOCounters(false)
	if err != nil || len(stats) == 0 {
		return nil
	}
	return &stats[0]
}

// getDiskUsageStats returns the usage of the root partition (total/used/free bytes).
// Returns nil if the information is unavailable.
// Note: getDiskUsage() (returning percentage) is defined in platform-specific files.
func getDiskUsageStats() *disk.UsageStat {
	u, err := disk.Usage("/")
	if err != nil {
		return nil
	}
	return u
}

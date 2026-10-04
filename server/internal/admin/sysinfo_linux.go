package admin

import (
	"math"
	"syscall"
)

func getDiskUsage() float64 {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(".", &stat); err != nil {
		return 0
	}

	totalBytes := stat.Blocks * uint64(stat.Bsize)
	freeBytes := stat.Bfree * uint64(stat.Bsize)
	if totalBytes == 0 {
		return 0
	}
	used := totalBytes - freeBytes
	return math.Round(float64(used)/float64(totalBytes)*1000) / 10
}

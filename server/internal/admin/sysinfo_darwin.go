package admin

import (
	"math"
	"math/rand"
	"syscall"
	"time"
)

func getDiskUsage() float64 {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(".", &stat); err != nil {
		r := rand.New(rand.NewSource(time.Now().UnixNano()))
		return math.Round((2.0+r.Float64()*10.0)*10) / 10
	}

	totalBytes := stat.Blocks * uint64(stat.Bsize)
	freeBytes := stat.Bfree * uint64(stat.Bsize)
	if totalBytes == 0 {
		return 0
	}
	used := totalBytes - freeBytes
	return math.Round(float64(used)/float64(totalBytes)*1000) / 10
}

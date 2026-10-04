package admin

import (
	"math"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

func getDiskUsage() float64 {
	path := "."
	if exe, err := os.Executable(); err == nil {
		path = filepath.Dir(exe)
	}

	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	getDiskFreeSpaceEx := kernel32.NewProc("GetDiskFreeSpaceExW")

	var freeBytes, totalBytes, totalFreeBytes int64
	ret, _, _ := getDiskFreeSpaceEx.Call(
		uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(path))),
		uintptr(unsafe.Pointer(&freeBytes)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFreeBytes)),
	)
	if ret == 0 {
		return 15.0
	}

	if totalBytes == 0 {
		return 0
	}
	used := totalBytes - freeBytes
	return math.Round(float64(used)/float64(totalBytes)*1000) / 10
}

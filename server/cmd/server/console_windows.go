//go:build windows

package main

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

var ownConsole bool

func init() {
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	procGetConsoleProcessList := kernel32.NewProc("GetConsoleProcessList")

	var processList [2]uint32
	ret, _, _ := procGetConsoleProcessList.Call(
		uintptr(unsafe.Pointer(&processList[0])),
		uintptr(len(processList)),
	)
	// ret = number of processes attached to this console.
	// 1 = only this process (double-click launch)
	// >= 2 = launched from a shell (cmd, PowerShell, etc.)
	if ret == 1 {
		ownConsole = true
	}
}

func exitWithPause(code int) {
	if ownConsole && code != 0 {
		fmt.Println("\nPress Enter to exit...")
		fmt.Scanln()
	}
	os.Exit(code)
}

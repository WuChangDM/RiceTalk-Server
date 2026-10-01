//go:build !windows

package main

import "os"

var ownConsole bool

func exitWithPause(code int) {
	os.Exit(code)
}

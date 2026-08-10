//go:build !darwin && !windows

package autostart

import (
	"fmt"
	"runtime"
)

func enablePlatform() error   { return fmt.Errorf("unsupported platform: %s", runtime.GOOS) }
func disablePlatform() error  { return fmt.Errorf("unsupported platform: %s", runtime.GOOS) }
func isEnabledPlatform() bool { return false }

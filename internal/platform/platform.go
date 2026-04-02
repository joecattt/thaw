package platform

import "runtime"

// IsDarwin returns true on macOS.
func IsDarwin() bool { return runtime.GOOS == "darwin" }

// IsLinux returns true on Linux.
func IsLinux() bool { return runtime.GOOS == "linux" }

// Name returns the platform name.
func Name() string { return runtime.GOOS + "/" + runtime.GOARCH }

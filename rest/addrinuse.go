//go:build !windows

package rest

import (
	"errors"
	"syscall"
)

// addrInUse reports whether a Listen error means the port is taken.
func addrInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE)
}

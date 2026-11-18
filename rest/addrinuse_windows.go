package rest

import (
	"errors"
	"syscall"
)

// wsaeaddrinuse is Winsock's "address already in use" (WSAEADDRINUSE).
// Windows reports a taken port with it, not with syscall.EADDRINUSE, which
// on Windows is an invented errno nothing returns.
const wsaeaddrinuse = syscall.Errno(10048)

// addrInUse reports whether a Listen error means the port is taken.
func addrInUse(err error) bool {
	return errors.Is(err, wsaeaddrinuse) || errors.Is(err, syscall.EADDRINUSE)
}

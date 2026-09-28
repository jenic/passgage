//go:build !windows

package gitstore

import (
	"net"
	"time"
)

func dialAgent(address string) (net.Conn, error) {
	return net.DialTimeout("unix", address, 5*time.Second)
}

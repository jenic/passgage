package gitstore

import (
	"net"
	"time"

	"github.com/Microsoft/go-winio"
)

func dialAgent(address string) (net.Conn, error) {
	timeout := 5 * time.Second
	return winio.DialPipe(address, &timeout)
}

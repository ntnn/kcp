package framework

import (
	"fmt"
	"net"
)

// unusedPort returns a TCP port that is available for binding.
func unusedPort() (int, func() error, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, nil, fmt.Errorf("could not bind to a port: %v", err)
	}
	return l.Addr().(*net.TCPAddr).Port, l.Close, nil
}

// unusedPorts returns three TCP ports that are available for binding.
func unusedPorts() (int, int, int, error) {
	port1, close1, err := unusedPort()
	if err != nil {
		return 0, 0, 0, err
	}
	defer close1() //nolint:errcheck

	port2, close2, err := unusedPort()
	if err != nil {
		return 0, 0, 0, err
	}
	defer close2() //nolint:errcheck

	port3, close3, err := unusedPort()
	if err != nil {
		return 0, 0, 0, err
	}
	defer close3() //nolint:errcheck

	return port1, port2, port3, nil
}

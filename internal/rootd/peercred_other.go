//go:build unix && !linux

package rootd

import (
	"errors"
	"net"
)

func peerCred(*net.UnixConn) (int, int, error) {
	return 0, 0, errors.New("提权助手只支持 Linux")
}

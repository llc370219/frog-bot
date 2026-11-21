//go:build cgo

package main

/*
#include <stdlib.h>
typedef const char cchar_t;
*/
import "C"

import (
	"encoding/json"
	"os/signal"
	"runtime/debug"
	"sync"
	"syscall"
	"unsafe"

	"frogbot/internal/remote"
)

func init() {
	debug.SetMemoryLimit(48 << 20)
	signal.Ignore(syscall.SIGPIPE)
}

var (
	tunMu   sync.Mutex
	nextID  int
	tunnels = map[int]*remote.ClientTunnel{}
)

type startResult struct {
	Handle     int    `json:"handle"`
	Port       int    `json:"port"`
	Mode       string `json:"mode"`
	ServerName string `json:"server_name"`
	Error      string `json:"error,omitempty"`
}

//export FrogBoxStart
func FrogBoxStart(uri *C.cchar_t, deviceName *C.cchar_t) *C.char {
	rawURI := C.GoString(uri)
	devName := C.GoString(deviceName)

	tun, err := remote.StartClientTunnel(rawURI, devName)
	if err != nil {
		b, _ := json.Marshal(startResult{Error: err.Error()})
		return C.CString(string(b))
	}

	tunMu.Lock()
	nextID++
	id := nextID
	tunnels[id] = tun
	tunMu.Unlock()

	b, _ := json.Marshal(startResult{
		Handle:     id,
		Port:       tun.Port(),
		Mode:       tun.Mode(),
		ServerName: tun.ServerName(),
	})
	return C.CString(string(b))
}

//export FrogBoxStop
func FrogBoxStop(handle C.int) {
	id := int(handle)
	tunMu.Lock()
	tun := tunnels[id]
	delete(tunnels, id)
	tunMu.Unlock()
	if tun != nil {
		_ = tun.Close()
	}
}

//export FrogBoxFree
func FrogBoxFree(ptr *C.char) {
	if ptr != nil {
		C.free(unsafe.Pointer(ptr))
	}
}

func main() {}

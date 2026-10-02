//go:build cgo && (linux || darwin || freebsd)

// The shared library speaks CLIProxyAPI C ABI v1 / JSON schema v6.
package main

/*
#include "bridge.h"
*/
import "C"

import (
	"encoding/json"
	"errors"
	"sync"
	"unsafe"

	"cliproxyapi-github-copilot/internal/plugin"
)

const maxRPCBytes = 64 << 20

var instanceMu sync.RWMutex
var instance *plugin.Plugin

type nativeHost struct{}

func (nativeHost) Call(method string, request any, response any) error {
	raw, err := json.Marshal(request)
	if err != nil || len(raw) > maxRPCBytes {
		return errors.New("cannot encode host callback")
	}
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	ptr := C.CBytes(raw)
	defer C.free(ptr)
	var out C.cliproxy_buffer
	code := C.call_host_api(cMethod, (*C.uint8_t)(ptr), C.size_t(len(raw)), &out)
	if out.ptr != nil {
		defer C.free_host_buffer(out.ptr, out.len)
	}
	if out.ptr == nil || out.len > maxRPCBytes {
		return errors.New("invalid host callback response")
	}
	data := C.GoBytes(out.ptr, C.int(out.len))
	var envelope struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(data, &envelope) != nil || !envelope.OK || code != 0 {
		return errors.New("host callback failed or request canceled")
	}
	if response != nil && len(envelope.Result) > 0 {
		if json.Unmarshal(envelope.Result, response) != nil {
			return errors.New("cannot decode host callback response")
		}
	}
	return nil
}

func main() {}

//export cliproxyGoInit
func cliproxyGoInit() C.int {
	instanceMu.Lock()
	instance = plugin.New(nativeHost{})
	instanceMu.Unlock()
	return 0
}

//export cliproxyGoCall
func cliproxyGoCall(method *C.char, request *C.uint8_t, size C.size_t, out *C.cliproxy_buffer) (code C.int) {
	if out == nil {
		return 1
	}
	out.ptr = nil
	out.len = 0
	defer func() {
		if recover() != nil {
			writeResponse(out, []byte(`{"ok":false,"error":{"code":"internal_error","message":"native plugin failure","http_status":500}}`))
			code = 1
		}
	}()
	if method == nil || size > maxRPCBytes || request == nil && size != 0 {
		return 1
	}
	instanceMu.RLock()
	p := instance
	instanceMu.RUnlock()
	if p == nil {
		return 1
	}
	raw := C.GoBytes(unsafe.Pointer(request), C.int(size))
	writeResponse(out, p.Dispatch(C.GoString(method), raw))
	return 0
}

func writeResponse(out *C.cliproxy_buffer, data []byte) {
	out.ptr = C.CBytes(data)
	if out.ptr != nil {
		out.len = C.size_t(len(data))
	}
}

//export cliproxyGoShutdown
func cliproxyGoShutdown() {
	instanceMu.Lock()
	p := instance
	instance = nil
	instanceMu.Unlock()
	if p != nil {
		p.Close()
	}
}

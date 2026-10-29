//go:build darwin && cgo

package macbundle

/*
#cgo darwin LDFLAGS: -framework CoreServices -framework Foundation
#include <CoreServices/CoreServices.h>
#include <stdlib.h>

// setDefaultHandlerCF 把 scheme 的默认处理器设为 bundleID。
// LSSetDefaultHandlerForURLScheme 虽被标记弃用，但仍是命令行工具（duti 等）
// 设置默认 URL 处理器的标准做法，macOS 26 上仍在系统里。
static OSStatus setDefaultHandlerCF(const char *scheme, const char *bundleID) {
	CFStringRef s = CFStringCreateWithCString(NULL, scheme, kCFStringEncodingUTF8);
	CFStringRef b = CFStringCreateWithCString(NULL, bundleID, kCFStringEncodingUTF8);
	if (s == NULL || b == NULL) {
		if (s != NULL) CFRelease(s);
		if (b != NULL) CFRelease(b);
		return -1;
	}
	OSStatus st = LSSetDefaultHandlerForURLScheme(s, b);
	CFRelease(s);
	CFRelease(b);
	return st;
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// setDefaultHandler 设置某个 URL scheme 的默认处理器（bundle 标识）。
func setDefaultHandler(scheme, bundleID string) error {
	cs := C.CString(scheme)
	cb := C.CString(bundleID)
	defer C.free(unsafe.Pointer(cs))
	defer C.free(unsafe.Pointer(cb))
	if st := C.setDefaultHandlerCF(cs, cb); st != 0 {
		return fmt.Errorf("设置 %s:// 默认处理器失败（OSStatus=%d）", scheme, int(st))
	}
	return nil
}

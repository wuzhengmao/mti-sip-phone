//go:build windows

package ui

import (
	"runtime"
	"syscall"
	"unsafe"
)

// showSystemFolderDialog 用系统文件夹选择对话框（IFileOpenDialog +
// FOS_PICKFOLDERS）选择目录。Fyne 自带的 dialog.FolderOpen 只能内嵌在主
// 窗体画布内（主窗体固定 320px 宽，空间很小），这里改用系统对话框。
//
// 在调用方 goroutine 上以 STA 打开（调用方负责放到后台 goroutine）：
// Show() 自带消息循环，会阻塞当前线程直到用户关闭对话框。
// 返回值区分三种结果：选定路径 / 用户取消（调用方不要回退）/
// 系统不可用（调用方回退到 Fyne 内置对话框）。
func showSystemFolderDialog(title, initial string) (string, folderDialogResult) {
	runtime.LockOSThread() // COM 按线程初始化，须固定线程
	defer runtime.UnlockOSThread()

	ole32 := syscall.NewLazyDLL("ole32.dll")
	coInitializeEx := ole32.NewProc("CoInitializeEx")
	coUninitialize := ole32.NewProc("CoUninitialize")
	coCreateInstance := ole32.NewProc("CoCreateInstance")
	coTaskMemFree := ole32.NewProc("CoTaskMemFree")

	// STA：通用文件对话框要求单线程套间
	hr, _, _ := coInitializeEx.Call(0, 0x2 /* COINIT_APARTMENTTHREADED */)
	if int32(hr) < 0 {
		return "", folderDialogUnavailable
	}
	defer coUninitialize.Call()

	// CLSID_FileOpenDialog {DC1C5A9C-E88A-4dde-A5A1-60F82A20AEF7}
	clsid := &guid{0xDC1C5A9C, 0xE88A, 0x4dde, [8]byte{0xA5, 0xA1, 0x60, 0xF8, 0x2A, 0x20, 0xAE, 0xF7}}
	// IID_IFileOpenDialog {d57c7288-d4ad-4768-be02-9d969532d960}
	iid := &guid{0xd57c7288, 0xd4ad, 0x4768, [8]byte{0xbe, 0x02, 0x9d, 0x96, 0x95, 0x32, 0xD9, 0x60}}
	var dlg uintptr
	hr, _, _ = coCreateInstance.Call(uintptr(unsafe.Pointer(clsid)), 0, 0x1, /* CLSCTX_INPROC_SERVER */
		uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&dlg)))
	if int32(hr) < 0 || dlg == 0 {
		return "", folderDialogUnavailable
	}
	defer comCall(dlg, 2) // Release

	// SetOptions：只选文件夹 + 必须是文件系统路径 + 路径必须存在
	const (
		fosPickFolders     = 0x00000020
		fosForceFileSystem = 0x00000040
		fosPathMustExist   = 0x00000800
	)
	comCall(dlg, 9 /* SetOptions */, fosPickFolders|fosForceFileSystem|fosPathMustExist)

	if title != "" {
		if p, err := syscall.UTF16PtrFromString(title); err == nil {
			comCall(dlg, 17 /* SetTitle */, uintptr(unsafe.Pointer(p)))
		}
	}
	if initial != "" {
		if item, ok := shellItemFromPath(initial); ok {
			comCall(dlg, 12 /* SetFolder */, item)
			comCall(item, 2) // Release
		}
	}

	// Show：用户取消返回 HRESULT_FROM_WIN32(ERROR_CANCELLED)——
	// 必须与"不可用"区分开，否则取消后会再弹一个内置对话框
	hr = comCall(dlg, 3 /* Show */, 0)
	if int32(hr) < 0 {
		const hresultCancelled = 0x800704C7 // HRESULT_FROM_WIN32(ERROR_CANCELLED)
		if uint32(hr) == hresultCancelled {
			return "", folderDialogCancelled
		}
		return "", folderDialogUnavailable
	}

	var item uintptr
	hr = comCall(dlg, 20 /* GetResult */, uintptr(unsafe.Pointer(&item)))
	if int32(hr) < 0 || item == 0 {
		return "", folderDialogUnavailable
	}
	defer comCall(item, 2) // Release

	const sigdnFileSysPath = 0x80058000
	var strPtr *uint16
	hr = comCall(item, 5 /* GetDisplayName */, sigdnFileSysPath, uintptr(unsafe.Pointer(&strPtr)))
	if int32(hr) < 0 || strPtr == nil {
		return "", folderDialogUnavailable
	}
	defer coTaskMemFree.Call(uintptr(unsafe.Pointer(strPtr)))

	// 到 NUL 为止的长度
	n := 0
	for p := unsafe.Pointer(strPtr); *(*uint16)(p) != 0; p = unsafe.Add(p, unsafe.Sizeof(uint16(0))) {
		n++
	}
	path := syscall.UTF16ToString(unsafe.Slice(strPtr, n))
	if path == "" {
		return "", folderDialogUnavailable
	}
	return path, folderDialogPicked
}

// comCall 调用 COM 接口方法：obj 为接口指针，idx 为 vtable 槽位下标
// （IUnknown 占 0/1/2），args 为其余参数。
// 注意：接口指针的首字段才是 vtable 地址——直接把接口指针当 vtable 起点
// 会整体错位并取到空指针（曾导致进程访问违例崩溃）。
func comCall(obj uintptr, idx int, args ...uintptr) uintptr {
	vtablePtr := *(*uintptr)(unsafe.Pointer(obj))
	methods := unsafe.Slice((*uintptr)(unsafe.Pointer(vtablePtr)), idx+1)
	all := append([]uintptr{obj}, args...)
	r, _, _ := syscall.SyscallN(methods[idx], all...)
	return r
}

// shellItemFromPath 用 SHCreateItemFromParsingName 从路径取 IShellItem。
func shellItemFromPath(path string) (uintptr, bool) {
	shell32 := syscall.NewLazyDLL("shell32.dll")
	create := shell32.NewProc("SHCreateItemFromParsingName")
	// IID_IShellItem {43826d1e-e718-42ee-bc55-a1e261c37bfe}
	iidItem := &guid{0x43826d1e, 0xe718, 0x42ee, [8]byte{0xbc, 0x55, 0xa1, 0xe2, 0x61, 0xc3, 0x7b, 0xfe}}
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, false
	}
	var item uintptr
	hr, _, _ := create.Call(uintptr(unsafe.Pointer(p)), 0,
		uintptr(unsafe.Pointer(iidItem)), uintptr(unsafe.Pointer(&item)))
	if int32(hr) < 0 || item == 0 {
		return 0, false
	}
	return item, true
}

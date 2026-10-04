//go:build windows

package ui

import (
	"runtime"
	"syscall"
	"unsafe"
)

// Windows 的文件打开/保存对话框（IFileOpenDialog / IFileSaveDialog）。
//
// 与目录选择（folderdlg_windows.go）同一套路：COM 通用文件对话框要 STA 线程 +
// 自带消息循环（Show 会阻塞当前线程，调用方负责放到后台 goroutine），取消要
// 与"不可用"分开。这里复用同文件的 guid / comCall / shellItemFromPath。
//
// 只用到 IFileDialog 的基础方法，所以保存对话框也按 IID_IFileDialog 取接口
// （IFileSaveDialog 由它派生，槽位顺序一致）：Show(3) / SetFileTypes(4) /
// SetOptions(9) / SetFileName(15) / SetTitle(17) / GetResult(20) /
// SetDefaultExtension(22)。GetResult 返回的 IShellItem 用 GetDisplayName(5) 取路径。

// IID_IFileDialog {42f85136-db7e-439c-85f1-e4075d135fc8}
var iidFileDialog = &guid{0x42f85136, 0xdb7e, 0x439c, [8]byte{0x85, 0xf1, 0xe4, 0x07, 0x5d, 0x13, 0x5f, 0xc8}}

// CLSID_FileSaveDialog {C0B4E2F3-BA21-4773-8DBA-335EC946EB8B}。
// 注意不是网上常见的 C0A651F5-…——那个 GUID 在本机注册表里不存在，
// CoCreateInstance 会返回 REGDB_E_CLASSNOTREG（0x80040154）。
var clsidFileSaveDialog = &guid{0xC0B4E2F3, 0xBA21, 0x4773, [8]byte{0x8D, 0xBA, 0x33, 0x5E, 0xC9, 0x46, 0xEB, 0x8B}}

// CLSID_FileOpenDialog {DC1C5A9C-E88A-4dde-A5A1-60F82A20AEF7}
var clsidFileOpenDialog = &guid{0xDC1C5A9C, 0xE88A, 0x4dde, [8]byte{0xA5, 0xA1, 0x60, 0xF8, 0x2A, 0x20, 0xAE, 0xF7}}

// IID_IFileOpenDialog {d57c7288-d4ad-4768-be02-9d969532d960}
var iidFileOpenDialog = &guid{0xd57c7288, 0xd4ad, 0x4768, [8]byte{0xbe, 0x02, 0x9d, 0x96, 0x95, 0x32, 0xD9, 0x60}}

// COMDLG_FILTERSPEC { LPCWSTR pszName; LPCWSTR pszSpec; }
type comdlgFilterSpec struct {
	name *uint16
	spec *uint16
}

// showSystemFileOpenDialog 选择要打开的文件（限定 CSV），返回完整路径。
func showSystemFileOpenDialog(title string) (string, fileDialogResult) {
	return showFileDialog(false, title, "")
}

// showSystemFileSaveDialog 选择保存位置（默认文件名 defaultName，限定 CSV）。
func showSystemFileSaveDialog(title, defaultName string) (string, fileDialogResult) {
	return showFileDialog(true, title, defaultName)
}

// showFileDialog 通用实现：save 决定用 IFileSaveDialog 还是 IFileOpenDialog。
func showFileDialog(save bool, title, defaultName string) (string, fileDialogResult) {
	runtime.LockOSThread() // COM 按线程初始化，须固定线程
	defer runtime.UnlockOSThread()

	ole32 := syscall.NewLazyDLL("ole32.dll")
	coInitializeEx := ole32.NewProc("CoInitializeEx")
	coUninitialize := ole32.NewProc("CoUninitialize")
	coCreateInstance := ole32.NewProc("CoCreateInstance")
	coTaskMemFree := ole32.NewProc("CoTaskMemFree")

	hr, _, _ := coInitializeEx.Call(0, 0x2 /* COINIT_APARTMENTTHREADED */)
	if int32(hr) < 0 {
		return "", fileDialogUnavailable
	}
	defer coUninitialize.Call()

	clsid, iid := clsidFileOpenDialog, iidFileOpenDialog
	if save {
		clsid, iid = clsidFileSaveDialog, iidFileDialog
	}
	var dlg uintptr
	hr, _, _ = coCreateInstance.Call(uintptr(unsafe.Pointer(clsid)), 0, 0x1, /* CLSCTX_INPROC_SERVER */
		uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&dlg)))
	if int32(hr) < 0 || dlg == 0 {
		return "", fileDialogUnavailable
	}
	defer comCall(dlg, 2) // Release

	// 只选文件系统路径 + 必须存在；保存时再要求"覆盖确认"与"非只读"
	const (
		fosOverwritePrompt = 0x00000002
		fosForceFileSystem = 0x00000040
		fosPathMustExist   = 0x00000800
		fosFileMustExist   = 0x00001000
		fosNoReadOnlyRet   = 0x00008000
	)
	opts := uintptr(fosForceFileSystem | fosPathMustExist)
	if save {
		opts |= fosOverwritePrompt | fosNoReadOnlyRet
	} else {
		opts |= fosFileMustExist
	}
	comCall(dlg, 9 /* SetOptions */, opts)

	// 文件类型过滤：CSV（导出/导入都只认这个格式）
	filterName, _ := syscall.UTF16PtrFromString("CSV 文件")
	filterSpec, _ := syscall.UTF16PtrFromString("*.csv")
	specs := []comdlgFilterSpec{{name: filterName, spec: filterSpec}}
	comCall(dlg, 4 /* SetFileTypes */, 1, uintptr(unsafe.Pointer(&specs[0])))

	if title != "" {
		if p, err := syscall.UTF16PtrFromString(title); err == nil {
			comCall(dlg, 17 /* SetTitle */, uintptr(unsafe.Pointer(p)))
		}
	}
	if save {
		if defaultName != "" {
			if p, err := syscall.UTF16PtrFromString(defaultName); err == nil {
				comCall(dlg, 15 /* SetFileName */, uintptr(unsafe.Pointer(p)))
			}
		}
		if p, err := syscall.UTF16PtrFromString("csv"); err == nil {
			comCall(dlg, 22 /* SetDefaultExtension */, uintptr(unsafe.Pointer(p)))
		}
	}

	// Show：用户取消返回 HRESULT_FROM_WIN32(ERROR_CANCELLED)——必须与"不可用"
	// 区分开，否则取消后会再弹一个内置对话框
	hr = comCall(dlg, 3 /* Show */, 0)
	if int32(hr) < 0 {
		const hresultCancelled = 0x800704C7
		if uint32(hr) == hresultCancelled {
			return "", fileDialogCancelled
		}
		return "", fileDialogUnavailable
	}

	var item uintptr
	hr = comCall(dlg, 20 /* GetResult */, uintptr(unsafe.Pointer(&item)))
	if int32(hr) < 0 || item == 0 {
		return "", fileDialogUnavailable
	}
	defer comCall(item, 2) // Release

	const sigdnFileSysPath = 0x80058000
	var strPtr *uint16
	hr = comCall(item, 5 /* GetDisplayName */, sigdnFileSysPath, uintptr(unsafe.Pointer(&strPtr)))
	if int32(hr) < 0 || strPtr == nil {
		return "", fileDialogUnavailable
	}
	defer coTaskMemFree.Call(uintptr(unsafe.Pointer(strPtr)))

	n := 0
	for p := unsafe.Pointer(strPtr); *(*uint16)(p) != 0; p = unsafe.Add(p, unsafe.Sizeof(uint16(0))) {
		n++
	}
	path := syscall.UTF16ToString(unsafe.Slice(strPtr, n))
	if path == "" {
		return "", fileDialogUnavailable
	}
	return path, fileDialogPicked
}

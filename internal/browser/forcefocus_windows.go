//go:build windows

package browser

import (
	"syscall"
	"unsafe"
)

var (
	user32                       = syscall.NewLazyDLL("user32.dll")
	procSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	procSetFocus                 = user32.NewProc("SetFocus")
	procGetForegroundWindow      = user32.NewProc("GetForegroundWindow")
	procBringWindowToTop         = user32.NewProc("BringWindowToTop")
	procSetCapture               = user32.NewProc("SetCapture")
	procReleaseCapture           = user32.NewProc("ReleaseCapture")
	procGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
	procAttachThreadInput        = user32.NewProc("AttachThreadInput")
	procGetCurrentThreadId       = user32.NewProc("GetCurrentThreadId")
)

// forceFocusToWindow forcibly sets input focus to the given HWND.
// It uses AttachThreadInput to steal focus from the current foreground
// window, which is needed because Windows normally prevents background
// windows from stealing focus.
//
// This is a workaround for the webview_go issue where WebView2 does not
// receive mouse click events when the window is activated programmatically
// (e.g. via schtasks in an RDP session).
func forceFocusToWindow(hwnd uintptr) {
	if hwnd == 0 {
		return
	}

	// Get current foreground window and its thread
	fgHwnd, _, _ := procGetForegroundWindow.Call()
	if fgHwnd == 0 {
		// No foreground window — just set focus
		procSetFocus.Call(hwnd)
		return
	}

	fgThreadID, _, _ := procGetWindowThreadProcessId.Call(fgHwnd, 0)
	currentThreadID, _, _ := procGetCurrentThreadId.Call()

	// Attach our thread input to the foreground window's thread so we can
	// steal focus
	if fgThreadID != currentThreadID {
		procAttachThreadInput.Call(fgThreadID, currentThreadID, 1)
		defer procAttachThreadInput.Call(fgThreadID, currentThreadID, 0)
	}

	// Bring to top and set focus
	procBringWindowToTop.Call(hwnd)
	procSetForegroundWindow.Call(hwnd)
	procSetFocus.Call(hwnd)
}

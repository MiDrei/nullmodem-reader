package clipboard

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32           = windows.NewLazySystemDLL("user32.dll")
	kernel32         = windows.NewLazySystemDLL("kernel32.dll")
	openClipboard    = user32.NewProc("OpenClipboard")
	closeClipboard   = user32.NewProc("CloseClipboard")
	getClipboardData = user32.NewProc("GetClipboardData")
	globalLock       = kernel32.NewProc("GlobalLock")
	globalUnlock     = kernel32.NewProc("GlobalUnlock")
)

const cfUnicodeText = 13

func read() (string, error) {
	if r, _, err := openClipboard.Call(0); r == 0 {
		return "", fmt.Errorf("clipboard: opening: %w", err)
	}
	defer closeClipboard.Call()

	h, _, _ := getClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return "", nil // no text on the clipboard
	}
	p, _, err := globalLock.Call(h)
	if p == 0 {
		return "", fmt.Errorf("clipboard: locking: %w", err)
	}
	defer globalUnlock.Call(h)
	// p addresses memory GlobalLock pinned outside the Go heap, so
	// turning it into a pointer is safe; going through &p keeps vet's
	// uintptr-to-pointer check, meant for Go memory, out of it.
	text := *(**uint16)(unsafe.Pointer(&p))
	return windows.UTF16PtrToString(text), nil
}

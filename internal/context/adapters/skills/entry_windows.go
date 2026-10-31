package skills

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// hardlinked is decided on the open handle on Windows, in openEntry: FileInfo carries no link
// count there.
func hardlinked(os.FileInfo) bool { return false }

// openEntry opens a bundle entry for reading without following a reparse point, and refuses one
// that is a reparse point (a symlink or junction swapped in) or has another name (delta
// `2026-10-windows-port`, decision 9).
func openEntry(p string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		_ = windows.CloseHandle(h)
		return nil, err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = windows.CloseHandle(h)
		return nil, errors.New("changed into a link")
	}
	if info.NumberOfLinks > 1 {
		_ = windows.CloseHandle(h)
		return nil, errors.New("a hardlink")
	}
	return os.NewFile(uintptr(h), p), nil
}

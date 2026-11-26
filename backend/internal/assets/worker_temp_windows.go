package assets

import (
	"syscall"
	"unsafe"
)

var getDiskFreeSpaceEx = syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")

func workerTempFreeBytes(directory string) (uint64, error) {
	path, err := syscall.UTF16PtrFromString(directory)
	if err != nil {
		return 0, err
	}
	var free uint64
	ok, _, err := getDiskFreeSpaceEx.Call(uintptr(unsafe.Pointer(path)), uintptr(unsafe.Pointer(&free)), 0, 0)
	if ok == 0 {
		return 0, err
	}
	return free, nil
}

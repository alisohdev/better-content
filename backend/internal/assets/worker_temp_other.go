//go:build !linux && !darwin && !freebsd && !windows

package assets

import "errors"

func workerTempFreeBytes(string) (uint64, error) {
	return 0, errors.New("worker temporary disk capacity is unsupported on this OS")
}

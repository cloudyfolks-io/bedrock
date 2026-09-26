//go:build !linux

package host

import "errors"

func DiskSpace(string) (Space, error) {
	return Space{}, errors.New("free space check needs linux")
}

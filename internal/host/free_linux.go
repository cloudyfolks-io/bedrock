package host

import "syscall"

func DiskSpace(path string) (Space, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return Space{}, err
	}
	return Space{FreeBytes: stat.Bavail * uint64(stat.Bsize), SizeBytes: stat.Blocks * uint64(stat.Bsize)}, nil
}

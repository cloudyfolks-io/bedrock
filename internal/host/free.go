package host

type Space struct {
	FreeBytes uint64
	SizeBytes uint64
}

func FreeBytes(path string) (uint64, error) {
	space, err := DiskSpace(path)
	return space.FreeBytes, err
}

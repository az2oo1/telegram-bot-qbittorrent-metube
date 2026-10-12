package disk

// FreeSpace returns the available free space in bytes for the given path.
// It uses platform-specific system calls.
func GetFreeSpace(path string) (uint64, error) {
	return freeSpace(path)
}

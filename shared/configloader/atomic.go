package configloader

import "os"

// AtomicWrite writes data to path via a temp file + rename, preserving mode.
func AtomicWrite(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	os.Remove(tmp)
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

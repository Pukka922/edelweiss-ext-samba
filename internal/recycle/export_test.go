package recycle

// SetRename replaces the renameat2 call for a test; the returned function puts the system call back.
func SetRename(f func(olddirfd int, oldpath string, newdirfd int, newpath string, flags uint) error) func() {
	old := renameat2
	renameat2 = f
	return func() { renameat2 = old }
}

// SetMaxScan replaces the scan limit of List for a test; the returned function puts it back.
func SetMaxScan(n int) func() {
	old := maxScan
	maxScan = n
	return func() { maxScan = old }
}

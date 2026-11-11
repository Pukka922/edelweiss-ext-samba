package acl

// SetMaxScan sets the cap of the walk of Read for a test and returns what puts it back.
func SetMaxScan(n int) func() {
	old := maxScan
	maxScan = n
	return func() { maxScan = old }
}

// SetNoTmpfile has the probe make a named file, as on a file system without O_TMPFILE, and returns what puts it back.
func SetNoTmpfile() func() {
	tmpfile = false
	return func() { tmpfile = true }
}

// SetProbeACLAttr sets the attribute that the probe writes for the POSIX ACLs, for a test, and returns what puts it back.
func SetProbeACLAttr(name string) func() {
	old := probeACLAttr
	probeACLAttr = name
	return func() { probeACLAttr = old }
}

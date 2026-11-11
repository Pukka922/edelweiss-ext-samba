package actions

// SetCountCap sets the cap of the count of files that needs_convert gives, for a test, and returns what puts it back.
func SetCountCap(n int) func() {
	old := countCap
	countCap = n
	return func() { countCap = old }
}

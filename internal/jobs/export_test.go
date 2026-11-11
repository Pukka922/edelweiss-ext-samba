package jobs

// SetIDRandom replaces the source of the random part of an id for a test, and returns what puts the old one back.
func SetIDRandom(f func([]byte)) (restore func()) {
	old := idRandom
	idRandom = f
	return func() { idRandom = old }
}

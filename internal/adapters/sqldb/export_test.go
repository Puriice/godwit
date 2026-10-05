package sqldb

// SetMaxRepeat lowers the repeat iteration cap for a test; call the result to
// restore it.
func SetMaxRepeat(n int) (restore func()) {
	old := maxRepeat
	maxRepeat = n
	return func() { maxRepeat = old }
}

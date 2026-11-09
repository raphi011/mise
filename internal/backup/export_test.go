package backup

// SetBeforeOpen installs fn as the hook addFile runs before opening a file,
// until the test ends.
func SetBeforeOpen(t interface{ Cleanup(func()) }, fn func(path string)) {
	old := beforeOpen
	beforeOpen = fn
	t.Cleanup(func() { beforeOpen = old })
}

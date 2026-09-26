// Package restrict used to confine the process with Landlock (Linux) and
// was stubbed out for shfm: rsync runs inside the file manager, where a
// process-wide Landlock ruleset would restrict every other feature too,
// and dropping it also drops the go-landlock and libcap/psx dependencies.
package restrict

// MaybeFileSystem is a no-op.
func MaybeFileSystem(_, _ []string) error { return nil }

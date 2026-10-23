package local

import "os"

// Windows has no portable directory fsync through os.File. Native development
// retains file Sync; use Linux containers for directory-entry crash durability.
func syncDirectory(_ *os.Root) error { return nil }

//go:build !unix

package agentio

import "os"

// foreignOwner has no portable ownership check outside Unix; agentio supports
// macOS and Linux, where owner_unix.go applies.
func foreignOwner(os.FileInfo) string { return "" }

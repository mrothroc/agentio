//go:build unix

package agentio

import (
	"fmt"
	"os"
	"syscall"
)

// foreignOwner returns why a directory owned by another user is refused, or "".
func foreignOwner(info os.FileInfo) string {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "has no readable owner; refusing"
	}
	if int(st.Uid) != os.Getuid() {
		return fmt.Sprintf("is owned by uid %d, not the current user; refusing", st.Uid)
	}
	return ""
}

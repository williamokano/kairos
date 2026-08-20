package local

import (
	"encoding/binary"
	"fmt"

	"golang.org/x/sys/unix"
)

// osBootID reads kern.boottime via sysctl — Darwin has no reboot-scoped
// UUID like Linux's boot_id, so the system boot timestamp itself (seconds
// since epoch) stands in: it changes at every reboot and is stable across
// process lifetimes within one boot.
type osBootID struct{}

func (osBootID) Current() (string, error) {
	raw, err := unix.SysctlRaw("kern.boottime")
	if err != nil {
		return "", fmt.Errorf("sysctl kern.boottime: %w", err)
	}
	if len(raw) < 8 {
		return "", fmt.Errorf("sysctl kern.boottime: short read (%d bytes)", len(raw))
	}
	sec := binary.LittleEndian.Uint64(raw[:8])
	return fmt.Sprintf("darwin-boottime-%d", sec), nil
}

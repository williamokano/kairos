package local

import (
	"fmt"
	"os"
	"strings"
)

// osBootID reads /proc/sys/kernel/random/boot_id — a UUID the kernel
// generates fresh at every boot.
type osBootID struct{}

func (osBootID) Current() (string, error) {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", fmt.Errorf("reading boot id: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

package local

// BootIDProvider returns an identifier that changes across a reboot — the
// first element of the (bootID, pgid, startedAt) identity triple
// (01-architecture.md). A pgid recorded under a previous boot ID belongs,
// at best, to a stranger's process; reconciliation must never signal it.
//
// The real implementation is platform-specific (bootid_linux.go,
// bootid_darwin.go) since there is no portable stdlib-only primitive for
// "an ID that changes at reboot."
type BootIDProvider interface {
	Current() (string, error)
}

// DefaultBootIDProvider returns the best available BootIDProvider for the
// current platform.
func DefaultBootIDProvider() BootIDProvider {
	return osBootID{}
}

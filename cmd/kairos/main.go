// Command kairos is the daemon, the executor, the CLI, and the TUI, all in
// one binary. This is the only file in the repository allowed to call
// os.Exit or log.Fatal — a stray Fatal anywhere else would kill a run
// mid-dispatch (AGENTS.md §2). Alongside internal/executor/local, it is
// also one of only two places permitted os/exec/syscall: starting the
// daemon itself (daemonstart_exec.go) is the binary launching its own
// second role, not a workflow actor process — see
// internal/cli/daemonstart.go and L04-daemon-api-cli.md's decision #4.
// serve.go is the composition root for the daemon's HTTP wiring, which
// must import internal/api — the one package no other package may import
// (dependencyDirection's "nothing imports internal/api" rule) except this
// one, its designated composition root.
package main

import (
	"os"

	"github.com/williamokano/kairos/internal/cli"
)

func main() {
	os.Exit(cli.Execute(os.Args[1:], daemonStarter{}, serve, runTUI))
}

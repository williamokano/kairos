// Package local is the one execution chokepoint (L4′): the only package in
// this repository allowed to import os/exec, syscall, or golang.org/x/sys.
// Every child process the daemon ever spawns is born here, in its own
// process group, recorded before fork/exec.
//
// Empty until L06 (local executor + workspaces). It exists now so
// TestArchitecture_noExecOutsideExecutor checks a real import graph.
package local

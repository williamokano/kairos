# L21 — hardening pass

Not a numbered build document (the plan ends at L20) — a running log of Future-work items from
L00-L20's own "Future work" sections and `11-limitations.md`'s NL-* register, picked up after the
full build plan shipped. Each entry: what was built, any real bug found and fixed.

## 1. NL-31 — close the illegal-transition trap at its remaining call sites (L07)

L07 fixed one instance of this bug (an admission `Denied` outcome tried to append
`NodeExecutionFailed` directly against a still-`Pending` exec, which `internal/domain`'s
`legalExecEvents` rejects — only `Executing` accepts `NodeExecutionFailed`) via a `denyNode`
helper that appends `NodeExecutionStarted` immediately before the failure. L07's own Future work
named the same trap as still open at `dispatchShellActor`'s and `dispatchLLMActor`'s pre-existing
early-failure returns.

Extracted the shared mechanism into `Engine.startThenFail` (`internal/engine/dispatch.go`) —
`denyNode`/`denyNodeWithReason` now call it too, rather than duplicating the append pair. Routed
every pre-start `appendNodeFailed` call in `actor_shell.go` (workspace-repo-missing, workspace
provisioning failure, process-start failure) and `actor_llm.go` (LLM-binary-missing, scratch-dir
creation, schema-file write, workspace provisioning, process-start) through `startThenFail`
instead. Found a fourth call site not named in L07's Future work but the identical bug:
`runActorDispatch`'s default case (an actor kind with no dispatch implementation) also called
`appendNodeFailed` directly against `Pending` — fixed the same way.

`denyNode`'s message carries a `"denied: "` prefix, appropriate for an actual admission/policy
refusal; the pre-start failures above are genuine runtime failures, not denials, so they call
`startThenFail` directly with an unprefixed message rather than borrowing `denyNode`'s wording.

**Real bug confirmed, not just theorized**: reverted the fix and ran the new test
(`TestEngine_preStartFailureNeverProducesAnIllegalTransition`,
`internal/engine/illegal_transition_test.go`) against the old code — it failed exactly as
predicted: `domain: illegal state transition`, and the run hung forever (`ExecPending` with no
recorded outcome, `RunRunning` forever, since nothing else moves it off that state). With the fix,
the same scenario reaches `RunFailed` with exactly one `ExecFailed` execution recorded.

Verification: `go build`/`go vet`/`gofmt -l .`/`golangci-lint run` clean, full `-race` suite green
(including a fresh, non-cached `cmd/kairos` run), `make cross` (4 platforms), `make arch` clean.

Committed as `<pending>`.

package registry

import "fmt"

// parseGates decodes a top-level `gates:` map (the workflow's own gate
// library, resolved against by every node's Gates []string) from doc's raw
// map. A definition with no `gates:` block returns a non-nil empty map, so
// callers never need a nil check.
func parseGates(topLevel map[string]any) (map[string]GateDef, error) {
	out := map[string]GateDef{}
	gatesAny, ok := topLevel["gates"].(map[string]any)
	if !ok {
		return out, nil
	}
	for id, v := range gatesAny {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("gates.%s: expected an object", id)
		}
		gd, err := parseGateDef(id, m)
		if err != nil {
			return nil, fmt.Errorf("gates.%s: %w", id, err)
		}
		out[id] = gd
	}
	return out, nil
}

func parseGateDef(id string, m map[string]any) (GateDef, error) {
	gd := GateDef{ID: id, Waivable: true}

	kind, _ := m["kind"].(string)
	switch GateKind(kind) {
	case GateExpr, GateCommand, GateFile, GateRegex, GateGitDiff, GateCoverage, GateJudged:
		gd.Kind = GateKind(kind)
	default:
		return GateDef{}, fmt.Errorf("unsupported kind %q (grounded/recipients/outbound-scan are 13-domains.md scope, not built yet)", kind)
	}

	gd.Severity, _ = m["severity"].(string)
	gd.Message, _ = m["message"].(string)
	if w, ok := m["waivable"].(bool); ok {
		gd.Waivable = w
	}

	check, _ := m["check"].(map[string]any)

	switch gd.Kind {
	case GateExpr:
		expr, _ := check["expr"].(string)
		if expr == "" {
			return GateDef{}, fmt.Errorf("kind: expr requires check.expr")
		}
		gd.Expr = expr

	case GateCommand:
		cmdAny, ok := check["command"].([]any)
		if !ok || len(cmdAny) == 0 {
			return GateDef{}, fmt.Errorf("kind: command requires a non-empty check.command list")
		}
		for _, c := range cmdAny {
			s, ok := c.(string)
			if !ok {
				return GateDef{}, fmt.Errorf("check.command entries must be strings")
			}
			gd.Command = append(gd.Command, s)
		}
		if wd, ok := check["workdir"].(string); ok {
			gd.Workdir = wd
		}
		gd.ExpectExitCode = 0
		if expect, ok := check["expect"].(map[string]any); ok {
			if ec, ok := expect["exitCode"].(float64); ok {
				gd.ExpectExitCode = int(ec)
			}
		}
		if timeoutStr, ok := check["timeout"].(string); ok && timeoutStr != "" {
			d, err := parseDuration(timeoutStr, 0)
			if err != nil {
				return GateDef{}, fmt.Errorf("check.timeout: %w", err)
			}
			gd.Timeout = d
		}
		if ff, ok := check["findingsFrom"].(map[string]any); ok {
			if format, ok := ff["format"].(string); ok {
				gd.FindingsFormat = format
			}
		}

	case GateFile:
		gd.FileExists = stringList(check["exists"])
		gd.FileAbsent = stringList(check["absent"])
		if len(gd.FileExists) == 0 && len(gd.FileAbsent) == 0 {
			return GateDef{}, fmt.Errorf("kind: file requires check.exists or check.absent")
		}

	case GateRegex:
		gd.RegexOver, _ = check["over"].(string)
		if gd.RegexOver != "added-lines" {
			return GateDef{}, fmt.Errorf("kind: regex requires check.over: \"added-lines\" (the only value this document implements — see Documented decisions)")
		}
		gd.RegexAbsent, _ = check["absent"].(string)
		if gd.RegexAbsent == "" {
			return GateDef{}, fmt.Errorf("kind: regex requires check.absent")
		}
		gd.RegexExclude = stringList(check["exclude"])

	case GateGitDiff:
		gd.GitDiffPathsForbidden = stringList(check["pathsForbidden"])
		gd.GitDiffMustTouch = stringList(check["mustTouch"])
		if mf, ok := check["maxFiles"].(float64); ok {
			gd.GitDiffMaxFiles = int(mf)
		}
		if ml, ok := check["maxLines"].(float64); ok {
			gd.GitDiffMaxLines = int(ml)
		}
		if nb, ok := check["noBinary"].(bool); ok {
			gd.GitDiffNoBinary = nb
		}
		if dv, ok := check["dirty"].(bool); ok {
			gd.GitDiffDirty = &dv
		}
		if sv, ok := check["staged"].(bool); ok {
			gd.GitDiffStaged = &sv
		}

	case GateCoverage:
		cmdAny, ok := check["command"].([]any)
		if !ok || len(cmdAny) == 0 {
			return GateDef{}, fmt.Errorf("kind: coverage requires a non-empty check.command list")
		}
		gd.Command = stringList(cmdAny)
		gd.CoverageThen = stringList(check["then"])
		if capture, ok := check["capture"].(map[string]any); ok {
			gd.CoverageCaptureRegex, _ = capture["regex"].(string)
		}
		if gd.CoverageCaptureRegex == "" {
			return GateDef{}, fmt.Errorf("kind: coverage requires check.capture.regex")
		}
		if expect, ok := check["expect"].(map[string]any); ok {
			if min, ok := expect["min"].(float64); ok {
				gd.CoverageMin = min
			}
		}

	case GateJudged:
		gd.JudgeLens, _ = check["lens"].(string)
		gd.JudgeFraming, _ = check["framing"].(string)
		if gd.JudgeFraming != "refutation" {
			return GateDef{}, fmt.Errorf("kind: judged requires check.framing: \"refutation\" (the only framing this document implements)")
		}
		if quorum, ok := check["quorum"].(map[string]any); ok {
			if of, ok := quorum["of"].(float64); ok {
				gd.JudgeQuorumOf = int(of)
			}
			gd.JudgeActors = stringList(quorum["from"])
		}
		if gd.JudgeQuorumOf == 0 || len(gd.JudgeActors) < gd.JudgeQuorumOf {
			return GateDef{}, fmt.Errorf("kind: judged requires check.quorum.of <= len(check.quorum.from)")
		}
	}

	return gd, nil
}

// stringList converts a decoded YAML/JSON []any of strings into []string,
// tolerating a bare string (some 05-gates.md examples write a single
// pattern rather than a list) and a nil/missing value (returns nil).
func stringList(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		return []string{t}
	default:
		return nil
	}
}

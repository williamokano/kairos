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
	case GateExpr, GateCommand:
		gd.Kind = GateKind(kind)
	default:
		return GateDef{}, fmt.Errorf("unsupported kind %q (L10-constraints-gates.md implements only expr and command)", kind)
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
	}

	return gd, nil
}

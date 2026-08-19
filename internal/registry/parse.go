package registry

import (
	"encoding/json"
	"fmt"
	"time"

	"sigs.k8s.io/yaml"
)

// yamlDefinition is the typed intermediate shape for the fields whose
// grammar is fixed (not shorthand-dynamic). Fields with a dynamic shape
// (output, inputs, on, wait, params) are decoded separately from the raw
// JSON via rawFields, since their value shapes vary (string vs object,
// scalar vs array) in ways encoding/json struct tags cannot express.
type yamlDefinition struct {
	Name   string      `json:"name"`
	Nodes  []yamlNode  `json:"nodes"`
	Limits *yamlLimits `json:"limits"`
}

type yamlNode struct {
	ID              string         `json:"id"`
	Actor           string         `json:"actor"`
	Prompt          string         `json:"prompt"`
	PromptFile      string         `json:"promptFile"`
	Context         []string       `json:"context"`
	Workspace       string         `json:"workspace"`
	WorkspacePaths  []string       `json:"workspacePaths"`
	HostExclusive   bool           `json:"hostExclusive"`
	Resources       *yamlResources `json:"resources"`
	Timeout         string         `json:"timeout"`
	SessionAffinity string         `json:"sessionAffinity"`
	Retry           *yamlRetry     `json:"retry"`
	Gates           []string       `json:"gates"`
	Effects         []string       `json:"effects"`
	Artifacts       []yamlArtifact `json:"artifacts"`
	Spawn           *yamlSpawn     `json:"spawn"`
	Join            *yamlJoin      `json:"join"`
	Optional        bool           `json:"optional"`
}

type yamlResources struct {
	Model *yamlModel `json:"model"`
}

type yamlModel struct {
	Class      string  `json:"class"`
	Slots      int     `json:"slots"`
	MaxCostUSD float64 `json:"maxCostUSD"`
}

type yamlRetry struct {
	MaxAttempts    int          `json:"maxAttempts"`
	RetryOn        []string     `json:"retryOn"`
	FreshWorkspace bool         `json:"freshWorkspace"`
	Mutate         []yamlMutate `json:"mutate"`
}

type yamlMutate struct {
	Attempt   int            `json:"attempt"`
	Actor     string         `json:"actor"`
	Resources *yamlResources `json:"resources"`
}

type yamlArtifact struct {
	Name   string `json:"name"`
	From   string `json:"from"`
	Kind   string `json:"kind"`
	Always bool   `json:"always"`
}

type yamlSpawn struct {
	Workflow         string `json:"workflow"`
	ForEach          string `json:"forEach"`
	Strategy         string `json:"strategy"`
	InheritWorkspace string `json:"inheritWorkspace"`
}

type yamlJoin struct {
	Mode           string `json:"mode"`
	OnChildFailure string `json:"onChildFailure"`
}

type yamlLimits struct {
	WallClock         string         `json:"wallClock"`
	MaxCostUSD        float64        `json:"maxCostUSD"`
	MaxNodeExecutions int            `json:"maxNodeExecutions"`
	MaxSpawnDepth     int            `json:"maxSpawnDepth"`
	LoopGuard         *yamlLoopGuard `json:"loopGuard"`
}

type yamlLoopGuard struct {
	MaxIterationsPerNode int    `json:"maxIterationsPerNode"`
	OnExceeded           string `json:"onExceeded"`
}

// raw is a parsed document decoded into map[string]any for the parts whose
// shape varies dynamically (output/inputs/on/wait/params shorthand) and for
// the denylist walk in validate.go.
type raw map[string]any

// rawDoc is the not-yet-defaulted, not-yet-validated result of Parse: the
// typed skeleton plus the raw map for dynamic fields.
type rawDoc struct {
	typed yamlDefinition
	raw   raw
}

// Parse decodes YAML bytes into a rawDoc. It performs no defaulting and no
// validation — ApplyDefaults and Validate are separate, explicit steps.
func Parse(data []byte) (rawDoc, error) {
	jsonBytes, err := yaml.YAMLToJSON(data)
	if err != nil {
		return rawDoc{}, fmt.Errorf("registry: converting YAML to JSON: %w", err)
	}

	var typed yamlDefinition
	if err := json.Unmarshal(jsonBytes, &typed); err != nil {
		return rawDoc{}, fmt.Errorf("registry: decoding definition: %w", err)
	}

	var m raw
	if err := json.Unmarshal(jsonBytes, &m); err != nil {
		return rawDoc{}, fmt.Errorf("registry: decoding definition as a map: %w", err)
	}

	return rawDoc{typed: typed, raw: m}, nil
}

// parseInputRef decodes one inputs: entry — the bare JSONPath string
// shorthand, or the long {path, default} form.
func parseInputRef(v any) (InputRef, error) {
	switch t := v.(type) {
	case string:
		return InputRef{Path: t}, nil
	case map[string]any:
		path, _ := t["path"].(string)
		if path == "" {
			return InputRef{}, fmt.Errorf("inputs entry missing 'path'")
		}
		ref := InputRef{Path: path}
		if def, ok := t["default"]; ok {
			b, err := json.Marshal(def)
			if err != nil {
				return InputRef{}, fmt.Errorf("marshalling default: %w", err)
			}
			ref.Default = &b
		}
		return ref, nil
	default:
		return InputRef{}, fmt.Errorf("inputs entry must be a string or {path, default} object, got %T", v)
	}
}

func parseDuration(s string, def time.Duration) (time.Duration, error) {
	if s == "" {
		return def, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", s, err)
	}
	return d, nil
}

// nodeMaps returns the raw per-node maps, in document order, for the
// dynamic-field extraction (output/inputs/on/wait) and the denylist walk.
func (d rawDoc) nodeMaps() []raw {
	nodesAny, _ := d.raw["nodes"].([]any)
	out := make([]raw, 0, len(nodesAny))
	for _, n := range nodesAny {
		if m, ok := n.(map[string]any); ok {
			out = append(out, raw(m))
		} else {
			out = append(out, raw{})
		}
	}
	return out
}

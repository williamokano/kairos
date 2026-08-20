package events

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/williamokano/kairos/internal/domain"
)

// ZeroFunc returns a fresh zero-value instance of a concrete domain.Event
// type, used as the target of json.Unmarshal.
type ZeroFunc func() domain.Event

type schemaEntry struct {
	schema *jsonschema.Schema
	zero   ZeroFunc
}

// Registry maps an event_type string to its known schema versions and the
// Go type that decodes it. An event type, once registered, is append-only
// across versions (AGENTS §4 rule 6): Register adds a version, it never
// replaces one.
type Registry struct {
	entries map[string]map[int]schemaEntry // eventType -> version -> entry
	newest  map[string]int
}

// NewRegistry returns an empty Registry. Building the built-in set is
// Builtin()'s job (init.go) — kept as an explicit constructor call rather
// than a package-level var, per AGENTS §3 (no package-level mutable state).
func NewRegistry() *Registry {
	return &Registry{
		entries: make(map[string]map[int]schemaEntry),
		newest:  make(map[string]int),
	}
}

// Register adds schema version `version` of eventType, compiled from
// schemaJSON (draft 2020-12), with zero constructing a fresh instance for
// json.Unmarshal. Registering the same (eventType, version) twice is an
// error — versions are append-only, never replaced.
func (r *Registry) Register(eventType string, version int, schemaJSON []byte, zero ZeroFunc) error {
	if _, exists := r.entries[eventType][version]; exists {
		return fmt.Errorf("events: %s v%d already registered", eventType, version)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaJSON))
	if err != nil {
		return fmt.Errorf("parsing schema for %s v%d: %w", eventType, version, err)
	}
	c := jsonschema.NewCompiler()
	url := fmt.Sprintf("mem://%s/v%d.json", eventType, version)
	if err := c.AddResource(url, doc); err != nil {
		return fmt.Errorf("adding schema resource for %s v%d: %w", eventType, version, err)
	}
	schema, err := c.Compile(url)
	if err != nil {
		return fmt.Errorf("compiling schema for %s v%d: %w", eventType, version, err)
	}
	if r.entries[eventType] == nil {
		r.entries[eventType] = make(map[int]schemaEntry)
	}
	r.entries[eventType][version] = schemaEntry{schema: schema, zero: zero}
	if version > r.newest[eventType] {
		r.newest[eventType] = version
	}
	return nil
}

// Validate checks payload (decoded JSON, e.g. via encoding/json.Unmarshal
// into any) against the schema registered for (eventType, version).
func (r *Registry) Validate(eventType string, version int, payload any) error {
	entry, ok := r.entries[eventType][version]
	if !ok {
		return fmt.Errorf("events: no schema registered for %s v%d", eventType, version)
	}
	return entry.schema.Validate(payload)
}

// New returns a fresh zero-value domain.Event for eventType at its newest
// registered version, and whether one was found.
func (r *Registry) New(eventType string) (domain.Event, bool) {
	version, ok := r.newest[eventType]
	if !ok {
		return nil, false
	}
	return r.entries[eventType][version].zero(), true
}

// NewVersion returns a fresh zero-value domain.Event for (eventType,
// version), and whether one was found.
func (r *Registry) NewVersion(eventType string, version int) (domain.Event, bool) {
	entry, ok := r.entries[eventType][version]
	if !ok {
		return nil, false
	}
	return entry.zero(), true
}

// CurrentVersion returns the newest registered schema version for
// eventType, and whether the type is known at all.
func (r *Registry) CurrentVersion(eventType string) (int, bool) {
	v, ok := r.newest[eventType]
	return v, ok
}

// Versions returns every registered version for eventType, ascending.
func (r *Registry) Versions(eventType string) []int {
	versions := make([]int, 0, len(r.entries[eventType]))
	for v := range r.entries[eventType] {
		versions = append(versions, v)
	}
	// small N (single digits per type); insertion-sort-simple is plenty.
	for i := 1; i < len(versions); i++ {
		for j := i; j > 0 && versions[j-1] > versions[j]; j-- {
			versions[j-1], versions[j] = versions[j], versions[j-1]
		}
	}
	return versions
}

// Types returns every registered event type name.
func (r *Registry) Types() []string {
	types := make([]string, 0, len(r.entries))
	for t := range r.entries {
		types = append(types, t)
	}
	return types
}

// Decode validates raw JSON payload against (eventType, version)'s schema,
// unmarshals it, and returns the concrete domain.Event **value** (not a
// pointer) — matching the value-receiver type switch domain.Advance uses.
// json.Unmarshal needs a pointer target, so New's zero value is a pointer;
// Decode is what turns that back into the value shape the rest of the
// system expects, in the one place that needs to know all sixteen types.
func (r *Registry) Decode(eventType string, version int, payload []byte) (domain.Event, error) {
	raw, err := jsonschema.UnmarshalJSON(bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("events: unmarshalling %s v%d for validation: %w", eventType, version, err)
	}
	if err := r.Validate(eventType, version, raw); err != nil {
		return nil, fmt.Errorf("events: %s v%d failed schema validation: %w", eventType, version, err)
	}
	ptr, ok := r.NewVersion(eventType, version)
	if !ok {
		return nil, fmt.Errorf("events: no constructor for %s v%d", eventType, version)
	}
	if err := json.Unmarshal(payload, ptr); err != nil {
		return nil, fmt.Errorf("events: unmarshalling %s v%d: %w", eventType, version, err)
	}
	return derefEvent(ptr), nil
}

// derefEvent turns the pointer json.Unmarshal filled in back into the
// value type domain.Advance's type switch matches. The switch is exactly
// the sixteen types init.go registers.
func derefEvent(ev domain.Event) domain.Event {
	switch e := ev.(type) {
	case *domain.TriggerReceived:
		return *e
	case *domain.RunStarted:
		return *e
	case *domain.RunRejected:
		return *e
	case *domain.RunCancelled:
		return *e
	case *domain.RunDegraded:
		return *e
	case *domain.RunDegradedResolved:
		return *e
	case *domain.NodeExecutionStarted:
		return *e
	case *domain.NodeOutputReceived:
		return *e
	case *domain.NodeWaitResolved:
		return *e
	case *domain.NodeGatesEvaluated:
		return *e
	case *domain.NodeExecutionFailed:
		return *e
	case *domain.NodeExecutionInterrupted:
		return *e
	case *domain.NodeExecutionLost:
		return *e
	case *domain.NodeExecutionAdopted:
		return *e
	case *domain.HumanTaskCreated:
		return *e
	case *domain.HumanTaskAnswered:
		return *e
	case *domain.EngineStarted:
		return *e
	case *domain.EngineStopped:
		return *e
	case *domain.EngineReconciled:
		return *e
	case *domain.ProcessOrphanReaped:
		return *e
	case *domain.LLMSessionStarted:
		return *e
	case *domain.SessionResumeFailed:
		return *e
	case *domain.SessionCostUnavailable:
		return *e
	case *domain.OutputRepairAttempted:
		return *e
	case *domain.LogDegraded:
		return *e
	case *domain.LogTruncated:
		return *e
	case *domain.ConstraintEvaluated:
		return *e
	case *domain.WaiverGranted:
		return *e
	case *domain.EffectConfirmationRequested:
		return *e
	case *domain.EffectConfirmed:
		return *e
	case *domain.ConversationMessageAppended:
		return *e
	default:
		return ev
	}
}

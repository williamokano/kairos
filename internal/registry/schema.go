package registry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// compileOutputShorthand compiles 03-workflows.md's output:/params: shorthand
// into a full draft-2020-12 JSON Schema. It is a leaf function of
// internal/registry, not shared with internal/events' schema compiler
// (a different concern: event-store schemas vs node I/O schemas) — no
// util/common package, per AGENTS.md §2.
//
// Grammar, read pragmatically from 03-workflows.md's illustrative (not
// rigorously specified) examples:
//   - a scalar value is a type token, optionally suffixed "!" for required:
//     "string!", "int", "bool", "number".
//   - an array value is exactly one element: the item type-token or nested
//     shape. The field itself is required whenever the item token is
//     required-suffixed (e.g. ["string!"]); the array's own presence adds
//     it to the parent's required list either way — array fields are
//     always required once authored, matching the common case in the
//     examples this shorthand exists to serve.
//   - a nested mapping value is a nested object schema, compiled
//     recursively by the same rules; nested-object fields are always
//     required (there is no scalar "!" position to attach to a mapping).
func compileOutputShorthand(fields map[string]any) (*jsonschema.Schema, error) {
	doc, err := shorthandToSchemaDoc(fields)
	if err != nil {
		return nil, err
	}
	return compileSchemaDoc(doc)
}

// shorthandToSchemaDoc builds the raw {"type":"object", "properties":...,
// "required":[...]} document (as any, matching jsonschema.UnmarshalJSON's
// decoded shape) from a shorthand fields map.
func shorthandToSchemaDoc(fields map[string]any) (map[string]any, error) {
	props := map[string]any{}
	var required []string
	for name, raw := range fields {
		propSchema, isRequired, err := shorthandField(raw)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", name, err)
		}
		props[name] = propSchema
		if isRequired {
			required = append(required, name)
		}
	}
	doc := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		doc["required"] = required
	}
	return doc, nil
}

func shorthandField(raw any) (schema map[string]any, required bool, err error) {
	switch v := raw.(type) {
	case string:
		return scalarTypeSchema(v)
	case []any:
		if len(v) != 1 {
			return nil, false, fmt.Errorf("array shorthand must have exactly one element, got %d", len(v))
		}
		item, itemRequired, err := shorthandField(v[0])
		if err != nil {
			return nil, false, fmt.Errorf("array item: %w", err)
		}
		return map[string]any{"type": "array", "items": item}, itemRequired, nil
	case map[string]any:
		nested, err := shorthandToSchemaDoc(v)
		if err != nil {
			return nil, false, err
		}
		return nested, true, nil
	default:
		return nil, false, fmt.Errorf("unsupported shorthand value %T", raw)
	}
}

func scalarTypeSchema(token string) (map[string]any, bool, error) {
	required := strings.HasSuffix(token, "!")
	base := strings.TrimSuffix(token, "!")
	jsonType, ok := map[string]string{
		"string": "string",
		"int":    "integer",
		"number": "number",
		"bool":   "boolean",
		"object": "object",
	}[base]
	if !ok {
		return nil, false, fmt.Errorf("unknown shorthand type %q", base)
	}
	return map[string]any{"type": jsonType}, required, nil
}

// compileSchemaDoc compiles a decoded (map[string]any) schema document into
// a *jsonschema.Schema, mirroring internal/events/registry.go's Register.
func compileSchemaDoc(doc map[string]any) (*jsonschema.Schema, error) {
	buf, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("marshalling schema doc: %w", err)
	}
	decoded, err := jsonschema.UnmarshalJSON(bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("decoding schema doc: %w", err)
	}
	c := jsonschema.NewCompiler()
	const url = "mem://registry/node-output.json"
	if err := c.AddResource(url, decoded); err != nil {
		return nil, fmt.Errorf("adding schema resource: %w", err)
	}
	schema, err := c.Compile(url)
	if err != nil {
		return nil, fmt.Errorf("compiling schema: %w", err)
	}
	return schema, nil
}

// permissiveSchema is the schema a node whose actor kind doesn't require a
// declared output (human, builtin.*) gets when it omits output/outputSchema
// — see the "outputSchema required" refinement in
// L03-definition-validator.md's Documented decisions.
func permissiveSchema() (*jsonschema.Schema, error) {
	return compileSchemaDoc(map[string]any{"type": "object"})
}

// Package jsondecode generates JSON methods for schemas with explicit int64
// conversion. It is shared by the OpenAPI, shared-schema, and task generators.
package jsondecode

import (
	"encoding/json"
	"fmt"
	"math"

	runtime "github.com/cloudcarver/anclax/lib/jsondecode"
	"github.com/getkin/kin-openapi/openapi3"
)

const Import = "github.com/cloudcarver/anclax/lib/jsondecode"

// Resolver resolves references that were not already loaded by kin-openapi.
type Resolver func(file string, ref *openapi3.SchemaRef) (string, *openapi3.SchemaRef, error)

// HasConversion reports whether a model contains an annotated int64 input.
func HasConversion(file string, ref *openapi3.SchemaRef, resolve Resolver) (bool, error) {
	b := builder{resolve: resolve}
	return b.hasConversion(file, ref, map[*openapi3.Schema]bool{})
}

// Method returns an UnmarshalJSON method and its compiled rule declaration, or
// an empty string when the model does not contain an annotated conversion.
func Method(file, name string, ref *openapi3.SchemaRef, resolve Resolver, inlineObjects bool) (string, error) {
	b := builder{resolve: resolve, active: map[*openapi3.Schema]bool{}, inlineObjects: inlineObjects}
	rule, err := b.build(file, ref, true)
	if err != nil || rule == nil {
		return "", err
	}
	data, err := json.Marshal(rule)
	if err != nil {
		return "", err
	}
	if _, err := runtime.Compile(string(data)); err != nil {
		return "", fmt.Errorf("%s JSON decoder: %w", name, err)
	}
	return fmt.Sprintf(`
var decode%sJSON = jsondecode.MustCompile(%q)

// UnmarshalJSON validates annotated inputs and converts them to their Go types.
func (v *%s) UnmarshalJSON(data []byte) error {
    type plain %s
    return decode%sJSON.Unmarshal(data, (*plain)(v))
}
`, name, string(data), name, name, name), nil
}

type builder struct {
	resolve       Resolver
	active        map[*openapi3.Schema]bool
	inlineObjects bool
}

func (b *builder) deref(file string, ref *openapi3.SchemaRef) (string, *openapi3.SchemaRef, error) {
	seen := map[*openapi3.SchemaRef]bool{}
	for ref != nil && ref.Value == nil && ref.Ref != "" {
		if b.resolve == nil || seen[ref] {
			return file, nil, fmt.Errorf("cannot resolve schema %s for JSON decoding", ref.Ref)
		}
		seen[ref] = true
		var err error
		file, ref, err = b.resolve(file, ref)
		if err != nil {
			return file, nil, err
		}
	}
	return file, ref, nil
}

func (b *builder) build(file string, ref *openapi3.SchemaRef, root bool) (*runtime.Rule, error) {
	delegate := !root && ref != nil && ref.Ref != ""
	file, ref, err := b.deref(file, ref)
	if err != nil || ref == nil || ref.Value == nil {
		return nil, err
	}
	schema := ref.Value
	if b.active[schema] {
		if delegate {
			has, err := b.hasConversion(file, ref, map[*openapi3.Schema]bool{})
			if err != nil {
				return nil, err
			}
			if has && schema.Type.Is("object") {
				return &runtime.Rule{Kind: "object", Nullable: schema.Nullable, Delegate: true}, nil
			}
		}
		return nil, nil
	}
	b.active[schema] = true
	defer delete(b.active, schema)
	rule := &runtime.Rule{Nullable: schema.Nullable}
	goType, _ := schema.Extensions["x-go-type"].(string)
	switch {
	case goType == "int64" && len(schema.AnyOf) > 0:
		rule.Kind = "int64"
		rule.Schema, err = b.scalar(file, schema, map[*openapi3.Schema]bool{})
		if err != nil {
			return nil, err
		}
		rule.Nullable = rule.Schema.VisitJSON(nil) == nil
	case goType != "":
		return nil, nil
	case schema.Type.Is("object") || len(schema.Properties) > 0:
		rule.Kind = "object"
		rule.Properties = map[string]runtime.Field{}
		if has, err := b.hasConversion(file, schema.AdditionalProperties.Schema, map[*openapi3.Schema]bool{}); err != nil {
			return nil, err
		} else if has {
			return nil, fmt.Errorf("int64 conversion in additionalProperties is not supported; use named object properties")
		}
		for name, prop := range schema.Properties {
			child, err := b.build(file, prop, false)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			if child == nil {
				continue
			}
			required := false
			for _, nameRequired := range schema.Required {
				required = required || nameRequired == name
			}
			rule.Properties[name] = runtime.Field{Rule: child, Required: required}
		}
		if len(rule.Properties) == 0 {
			return nil, nil
		}
		if !root && !delegate {
			if !b.inlineObjects {
				return nil, fmt.Errorf("nested objects with int64 conversion must use a named schema via $ref")
			}
			delegate = true
		}
	case schema.Type.Is("array"):
		rule.Kind = "array"
		rule.Items, err = b.build(file, schema.Items, false)
		if err != nil || rule.Items == nil {
			return nil, err
		}
	default:
		return nil, nil
	}
	if delegate {
		// The referenced model owns its conversion. Validating its converted
		// value again would apply the wrong anyOf branch to the original input.
		return &runtime.Rule{Kind: rule.Kind, Nullable: rule.Nullable, Delegate: true}, nil
	}
	return rule, nil
}

func (b *builder) hasConversion(file string, ref *openapi3.SchemaRef, seen map[*openapi3.Schema]bool) (bool, error) {
	file, ref, err := b.deref(file, ref)
	if err != nil || ref == nil || ref.Value == nil || seen[ref.Value] {
		return false, err
	}
	schema := ref.Value
	seen[schema] = true
	if goType, _ := schema.Extensions["x-go-type"].(string); goType != "" {
		return goType == "int64" && len(schema.AnyOf) > 0, nil
	}
	for _, child := range schema.Properties {
		if has, err := b.hasConversion(file, child, seen); err != nil || has {
			return has, err
		}
	}
	if has, err := b.hasConversion(file, schema.Items, seen); err != nil || has {
		return has, err
	}
	return b.hasConversion(file, schema.AdditionalProperties.Schema, seen)
}

func (b *builder) scalar(file string, schema *openapi3.Schema, seen map[*openapi3.Schema]bool) (*openapi3.Schema, error) {
	if seen[schema] {
		return nil, fmt.Errorf("recursive int64 input schema is not supported")
	}
	seen[schema] = true
	defer delete(seen, schema)
	copy := *schema
	copy.Extensions = nil
	if len(schema.Properties) > 0 || schema.Items != nil || schema.AdditionalProperties.Schema != nil {
		return nil, fmt.Errorf("int64 conversion requires scalar anyOf branches")
	}
	// kin-openapi stores numeric constraints as float64. Reject values whose
	// original integer precision cannot be recovered instead of silently
	// validating a rounded enum or bound. Input int64 values remain unrestricted.
	for _, v := range []*float64{schema.Min, schema.Max, schema.MultipleOf} {
		if v != nil && (math.IsInf(*v, 0) || math.IsNaN(*v) || math.Abs(*v) > 1<<53-1) {
			return nil, fmt.Errorf("int64 schema numeric constraints must be within the exact float64 integer range; use string constraints for larger values")
		}
	}
	for _, v := range schema.Enum {
		if n, ok := v.(float64); ok && math.Abs(n) > 1<<53-1 {
			return nil, fmt.Errorf("int64 schema numeric enum exceeds exact float64 integer range; use string enum values")
		}
	}
	for _, pair := range []struct {
		in  openapi3.SchemaRefs
		out *openapi3.SchemaRefs
	}{{schema.AnyOf, &copy.AnyOf}, {schema.OneOf, &copy.OneOf}, {schema.AllOf, &copy.AllOf}} {
		*pair.out = nil
		for _, child := range pair.in {
			resolved, err := b.scalarRef(file, child, seen)
			if err != nil {
				return nil, err
			}
			*pair.out = append(*pair.out, resolved)
		}
	}
	if schema.Not != nil {
		var err error
		copy.Not, err = b.scalarRef(file, schema.Not, seen)
		if err != nil {
			return nil, err
		}
	}
	return &copy, nil
}

func (b *builder) scalarRef(file string, ref *openapi3.SchemaRef, seen map[*openapi3.Schema]bool) (*openapi3.SchemaRef, error) {
	file, ref, err := b.deref(file, ref)
	if err != nil {
		return nil, err
	}
	if ref == nil || ref.Value == nil {
		return nil, fmt.Errorf("missing int64 input schema")
	}
	schema, err := b.scalar(file, ref.Value, seen)
	return &openapi3.SchemaRef{Value: schema}, err
}

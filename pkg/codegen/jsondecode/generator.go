// Package jsondecode generates JSON methods for schemas with explicit int64
// conversion. It is shared by the OpenAPI, shared-schema, and task generators.
package jsondecode

import (
	"context"
	"fmt"
	"math"

	"github.com/getkin/kin-openapi/openapi3"
)

const runtimeImport = "github.com/cloudcarver/anclax/lib/jsondecode"

// Model supplies the Go names and types chosen by the enclosing generator.
type Model struct {
	Name       string
	Underlying string
	Fields     []Field
}

type Field struct {
	JSONName string
	GoName   string
	Type     string
}

type Generated struct {
	Code    string
	Imports []string
}

// Rules exist only during generation. They are lowered to Go statements and
// expressions; generated programs do not retain or interpret schema objects.
type rule struct {
	Kind       string
	Nullable   bool
	Delegate   bool
	Schema     *openapi3.Schema
	Properties map[string]fieldRule
	Items      *rule
}

type fieldRule struct {
	Rule     *rule
	Required bool
}

// Resolver resolves references that were not already loaded by kin-openapi.
type Resolver func(file string, ref *openapi3.SchemaRef) (string, *openapi3.SchemaRef, error)

// HasConversion reports whether a model contains an annotated int64 input.
func HasConversion(file string, ref *openapi3.SchemaRef, resolve Resolver) (bool, error) {
	b := builder{resolve: resolve}
	return b.hasConversion(file, ref, map[*openapi3.Schema]bool{})
}

// Method emits model-specific decoding and validation code. An empty result
// means the model does not contain an annotated conversion.
func Method(file string, model Model, ref *openapi3.SchemaRef, resolve Resolver, inlineObjects bool) (Generated, error) {
	b := builder{resolve: resolve, active: map[*openapi3.Schema]bool{}, inlineObjects: inlineObjects}
	rule, err := b.build(file, ref, true)
	if err != nil || rule == nil {
		return Generated{}, err
	}
	return emit(model, rule)
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

func (b *builder) build(file string, ref *openapi3.SchemaRef, root bool) (*rule, error) {
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
				return &rule{Kind: "object", Nullable: schema.Nullable, Delegate: true}, nil
			}
		}
		return nil, nil
	}
	b.active[schema] = true
	defer delete(b.active, schema)
	node := &rule{Nullable: schema.Nullable}
	goType, _ := schema.Extensions["x-go-type"].(string)
	switch {
	case goType == "int64" && len(schema.AnyOf) > 0:
		node.Kind = "int64"
		node.Schema, err = b.scalar(file, schema, map[*openapi3.Schema]bool{})
		if err != nil {
			return nil, err
		}
		if err := node.Schema.Validate(context.Background()); err != nil {
			return nil, err
		}
		node.Nullable = node.Schema.VisitJSON(nil) == nil
	case goType != "":
		return nil, nil
	case schema.Type.Is("object") || len(schema.Properties) > 0:
		node.Kind = "object"
		node.Properties = map[string]fieldRule{}
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
			node.Properties[name] = fieldRule{Rule: child, Required: required}
		}
		if len(node.Properties) == 0 {
			return nil, nil
		}
		if !root && !delegate {
			if !b.inlineObjects {
				return nil, fmt.Errorf("nested objects with int64 conversion must use a named schema via $ref")
			}
			delegate = true
		}
	case schema.Type.Is("array"):
		node.Kind = "array"
		node.Items, err = b.build(file, schema.Items, false)
		if err != nil || node.Items == nil {
			return nil, err
		}
	default:
		return nil, nil
	}
	if delegate {
		// The referenced model owns its conversion. Validating its converted
		// value again would apply the wrong anyOf branch to the original input.
		return &rule{Kind: node.Kind, Nullable: node.Nullable, Delegate: true}, nil
	}
	return node, nil
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

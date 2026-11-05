package jsondecode

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"sort"
	"strconv"
	"strings"
)

type emitter struct {
	model    Model
	imports  map[string]bool
	helpers  strings.Builder
	patterns map[string]string
	next     int
}

func emit(model Model, node *rule) (Generated, error) {
	e := &emitter{model: model, imports: map[string]bool{}, patterns: map[string]string{}}
	e.use("bytes", "encoding/json", "fmt")
	var method strings.Builder
	fmt.Fprintf(&method, "\n// UnmarshalJSON validates annotated inputs and converts them to their Go types.\nfunc (v *%s) UnmarshalJSON(data []byte) error {\n", model.Name)
	if node.Kind == "object" {
		if err := e.object(&method, node); err != nil {
			return Generated{}, err
		}
	} else {
		// Public methods can also be called directly, without encoding/json's
		// syntax check. Object/array decoding performs that check itself.
		if node.Kind == "int64" {
			method.WriteString("if !json.Valid(data) { return fmt.Errorf(\"invalid JSON\") }\n")
		}
		resultType := model.Name
		if node.Kind == "int64" {
			resultType = "int64"
		}
		decoder, err := e.decoder(node, resultType, model.Underlying)
		if err != nil {
			return Generated{}, err
		}
		fmt.Fprintf(&method, "value, err := %s(data)\nif err != nil { return err }\n*v = %s(value)\nreturn nil\n", decoder, model.Name)
	}
	method.WriteString("}\n")
	code := method.String() + e.helpers.String()
	parsed, err := parser.ParseFile(token.NewFileSet(), "decoder.go", "package generated\n"+code, 0)
	if err != nil {
		return Generated{}, fmt.Errorf("%s: invalid generated decoder: %w", model.Name, err)
	}
	used := map[string]bool{}
	ast.Inspect(parsed, func(node ast.Node) bool {
		if selector, ok := node.(*ast.SelectorExpr); ok {
			if ident, ok := selector.X.(*ast.Ident); ok {
				used[ident.Name] = true
			}
		}
		return true
	})
	// Constant folding can discard a branch's length/regexp checks. Keep only
	// imports and precompiled patterns referenced by the final expressions.
	var globals []string
	for pattern, name := range e.patterns {
		if used[name] {
			globals = append(globals, fmt.Sprintf("\nvar %s = regexp.MustCompile(%q)\n", name, pattern))
			used["regexp"] = true
		}
	}
	sort.Strings(globals)
	imports := make([]string, 0, len(e.imports))
	for name := range e.imports {
		if used[path.Base(name)] {
			imports = append(imports, name)
		}
	}
	sort.Strings(imports)
	return Generated{Code: strings.Join(globals, "") + code, Imports: imports}, nil
}

func (e *emitter) use(imports ...string) {
	for _, name := range imports {
		e.imports[name] = true
	}
}

func (e *emitter) symbol() string {
	name := fmt.Sprintf("decode%sJSON%d", e.model.Name, e.next)
	e.next++
	return name
}

func (e *emitter) object(out *strings.Builder, node *rule) error {
	e.use(runtimeImport)
	out.WriteString("data = bytes.TrimSpace(data)\nif bytes.Equal(data, []byte(\"null\")) {\n")
	if node.Nullable {
		out.WriteString("return nil\n")
	} else {
		out.WriteString("return fmt.Errorf(\"null is not allowed\")\n")
	}
	out.WriteString("}\n")
	fmt.Fprintf(out, "type plain %s\nnext := plain(*v)\nwire := struct {\n*plain\n", e.model.Name)
	var fields []Field
	for _, field := range e.model.Fields {
		if _, ok := node.Properties[field.JSONName]; ok {
			fields = append(fields, field)
		}
	}
	if len(fields) != len(node.Properties) {
		return fmt.Errorf("%s: missing Go field bindings for JSON decoding", e.model.Name)
	}
	for i, field := range fields {
		fmt.Fprintf(out, "Field%d jsondecode.RawValue `json:%s`\n", i, strconv.Quote(field.JSONName))
	}
	out.WriteString("}{plain: &next}\nif err := json.Unmarshal(data, &wire); err != nil { return err }\n")
	for i, field := range fields {
		binding := node.Properties[field.JSONName]
		child := binding.Rule
		raw := fmt.Sprintf("wire.Field%d", i)
		if binding.Required {
			fmt.Fprintf(out, "if len(%s) == 0 { return fmt.Errorf(%q) }\n", raw, field.JSONName+": required field is missing")
		}
		fmt.Fprintf(out, "if len(%s) != 0 {\nif bytes.Equal(%s, []byte(\"null\")) {\n", raw, raw)
		pointer := strings.HasPrefix(field.Type, "*")
		if !child.Nullable || child.Kind == "int64" && !pointer {
			fmt.Fprintf(out, "return fmt.Errorf(%q)\n", field.JSONName+": null is not allowed")
		} else if pointer || child.Kind == "array" {
			fmt.Fprintf(out, "next.%s = nil\n", field.GoName)
		}
		out.WriteString("} else {\n")
		if child.Delegate {
			// Referenced models validate their original input in their own method.
			fmt.Fprintf(out, "if err := json.Unmarshal(%s, &next.%s); err != nil { return fmt.Errorf(%q, err) }\n", raw, field.GoName, field.JSONName+": %w")
		} else {
			valueType := strings.TrimPrefix(field.Type, "*")
			decoder, err := e.decoder(child, valueType, valueType)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "value, err := %s(%s)\nif err != nil { return fmt.Errorf(%q, err) }\n", decoder, raw, field.JSONName+": %w")
			if pointer {
				fmt.Fprintf(out, "next.%s = &value\n", field.GoName)
			} else {
				fmt.Fprintf(out, "next.%s = value\n", field.GoName)
			}
		}
		out.WriteString("}\n}\n")
	}
	fmt.Fprintf(out, "*v = %s(next)\nreturn nil\n", e.model.Name)
	return nil
}

func (e *emitter) decoder(node *rule, goType, underlying string) (string, error) {
	name := e.symbol()
	var out strings.Builder
	fmt.Fprintf(&out, "\nfunc %s(data []byte) (%s, error) {\nvar value %s\ndata = bytes.TrimSpace(data)\n", name, goType, goType)
	out.WriteString("if len(data) == 0 { return value, fmt.Errorf(\"missing JSON value\") }\nif bytes.Equal(data, []byte(\"null\")) {\n")
	if node.Nullable && node.Kind != "int64" {
		out.WriteString("return value, nil\n")
	} else {
		out.WriteString("return value, fmt.Errorf(\"null is not allowed\")\n")
	}
	out.WriteString("}\n")
	switch {
	case node.Delegate:
		out.WriteString("err := json.Unmarshal(data, &value)\nreturn value, err\n")
	case node.Kind == "int64":
		stringPredicate, err := e.predicate(node.Schema, "string", "text")
		if err != nil {
			return "", err
		}
		numberPredicate, err := e.predicate(node.Schema, "integer", "value")
		if err != nil {
			return "", err
		}
		out.WriteString("if data[0] == '\"' {\n")
		if stringPredicate == "false" {
			out.WriteString("return value, fmt.Errorf(\"string does not match input schema\")\n")
		} else {
			e.use("strconv")
			out.WriteString("var text string\nif err := json.Unmarshal(data, &text); err != nil { return value, err }\n")
			if stringPredicate != "true" {
				fmt.Fprintf(&out, "if !(%s) { return value, fmt.Errorf(\"string does not match input schema\") }\n", stringPredicate)
			}
			out.WriteString("return strconv.ParseInt(text, 10, 64)\n")
		}
		out.WriteString("}\n")
		if numberPredicate == "false" {
			out.WriteString("return value, fmt.Errorf(\"number does not match input schema\")\n")
		} else {
			e.use(runtimeImport)
			out.WriteString("if data[0] != '-' && (data[0] < '0' || data[0] > '9') { return value, fmt.Errorf(\"expected integer or string\") }\n")
			out.WriteString("value, err := jsondecode.ParseInt64Number(string(data))\nif err != nil { return value, err }\n")
			if numberPredicate != "true" {
				fmt.Fprintf(&out, "if !(%s) { return value, fmt.Errorf(\"number does not match input schema\") }\n", numberPredicate)
			}
			out.WriteString("return value, nil\n")
		}
	case node.Kind == "array":
		if !strings.HasPrefix(underlying, "[]") {
			return "", fmt.Errorf("%s: expected a Go slice, got %s", e.model.Name, underlying)
		}
		itemType := strings.TrimPrefix(underlying, "[]")
		decoder, err := e.decoder(node.Items, itemType, itemType)
		if err != nil {
			return "", err
		}
		out.WriteString("var items []json.RawMessage\nif err := json.Unmarshal(data, &items); err != nil { return value, err }\n")
		fmt.Fprintf(&out, "value = make(%s, len(items))\nfor i, raw := range items {\nitem, err := %s(raw)\nif err != nil { return nil, fmt.Errorf(\"item %%d: %%w\", i, err) }\nvalue[i] = item\n}\nreturn value, nil\n", goType, decoder)
	default:
		return "", fmt.Errorf("unsupported decoder kind %q", node.Kind)
	}
	out.WriteString("}\n")
	e.helpers.WriteString(out.String())
	return name, nil
}

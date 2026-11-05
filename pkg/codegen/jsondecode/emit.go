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
	numbers  map[string]string
	next     int
}

func emit(model Model, node *rule) (Generated, error) {
	if node.isScalar() && node.Target.Kind == "custom" && node.Target.Type == model.Name {
		return Generated{}, fmt.Errorf("%s: custom type and generated model must have different names", model.Name)
	}
	e := &emitter{model: model, imports: map[string]bool{}, patterns: map[string]string{}, numbers: map[string]string{}}
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
		if node.isScalar() || node.Kind == "map" {
			method.WriteString("if !json.Valid(data) { return fmt.Errorf(\"invalid JSON\") }\n")
		}
		resultType := model.Name
		if node.isScalar() {
			resultType = node.Target.Type
		}
		decoder, err := e.decoder(node, resultType, model.Underlying)
		if err != nil {
			return Generated{}, err
		}
		fmt.Fprintf(&method, "value, err := %s(data)\nif err != nil { return err }\n*v = %s(value)\nreturn nil\n", decoder, model.Name)
	}
	method.WriteString("}\n")
	if node.isScalar() && node.Target.Kind == "custom" {
		fmt.Fprintf(&method, "\nfunc (v %s) MarshalJSON() ([]byte, error) { value := %s(v); return json.Marshal(&value) }\n", model.Name, node.Target.Type)
	}
	code := method.String() + e.helpers.String()
	parsed, err := parser.ParseFile(token.NewFileSet(), "decoder.go", "package generated\n"+code, 0)
	if err != nil {
		return Generated{}, fmt.Errorf("%s: invalid generated decoder: %w", model.Name, err)
	}
	used := map[string]bool{}
	ast.Inspect(parsed, func(node ast.Node) bool {
		if ident, ok := node.(*ast.Ident); ok {
			used[ident.Name] = true
		}
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
	for number, name := range e.numbers {
		if used[name] {
			globals = append(globals, fmt.Sprintf("\nvar %s = jsondecode.NumberFromJSON(%q)\n", name, number))
			used["jsondecode"] = true
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
	typed := make([]bool, len(fields))
	for i, field := range fields {
		child := node.Properties[field.JSONName].Rule
		typed[i] = child.isScalar() && !child.Delegate && child.Target.Kind != "custom" && child.Target.Converter == ""
		wireType := "jsondecode.RawValue"
		if typed[i] {
			var err error
			wireType, err = e.scalarField(child, field)
			if err != nil {
				return err
			}
		}
		fmt.Fprintf(out, "Field%d %s `json:%s`\n", i, wireType, strconv.Quote(field.JSONName))
	}
	out.WriteString("}{plain: &next}\nif err := json.Unmarshal(data, &wire); err != nil { return err }\n")
	for i, field := range fields {
		binding := node.Properties[field.JSONName]
		child := binding.Rule
		raw := fmt.Sprintf("wire.Field%d", i)
		if typed[i] {
			if binding.Required {
				fmt.Fprintf(out, "if !%s.Present { return fmt.Errorf(%q) }\n", raw, field.JSONName+": required field is missing")
			}
			fmt.Fprintf(out, "if %s.Present {\n", raw)
			if strings.HasPrefix(field.Type, "*") {
				if child.Nullable {
					fmt.Fprintf(out, "if %s.Null { next.%s = nil } else {\n", raw, field.GoName)
				}
				// Keep the optional value separate: pointing inside wire would
				// retain its other fields and the embedded plain model as well.
				fmt.Fprintf(out, "value := %s.Value\nnext.%s = &value\n", raw, field.GoName)
				if child.Nullable {
					out.WriteString("}\n")
				}
			} else {
				fmt.Fprintf(out, "next.%s = %s.Value\n", field.GoName, raw)
			}
			out.WriteString("}\n")
			continue
		}
		if binding.Required {
			fmt.Fprintf(out, "if len(%s) == 0 { return fmt.Errorf(%q) }\n", raw, field.JSONName+": required field is missing")
		}
		fmt.Fprintf(out, "if len(%s) != 0 {\nif bytes.Equal(%s, []byte(\"null\")) {\n", raw, raw)
		pointer := strings.HasPrefix(field.Type, "*")
		if !child.Nullable || child.isScalar() && child.Target.Kind != "custom" && !pointer {
			fmt.Fprintf(out, "return fmt.Errorf(%q)\n", field.JSONName+": null is not allowed")
		} else if pointer || child.Kind == "array" || child.Kind == "map" {
			fmt.Fprintf(out, "next.%s = nil\n", field.GoName)
		} else {
			if err := e.fieldConversion(out, child, field, raw); err != nil {
				return err
			}
		}
		out.WriteString("} else {\n")
		if err := e.fieldConversion(out, child, field, raw); err != nil {
			return err
		}
		out.WriteString("}\n}\n")
	}
	fmt.Fprintf(out, "*v = %s(next)\nreturn nil\n", e.model.Name)
	return nil
}

// scalarField decodes a built-in scalar while encoding/json owns the token.
// It retains only the typed result and presence flags, avoiding a RawValue copy.
// References and custom converters keep their existing capture/delegation path.
func (e *emitter) scalarField(node *rule, field Field) (string, error) {
	name := e.symbol() + "Field"
	valueType := strings.TrimPrefix(field.Type, "*")
	decoder, err := e.decoder(node, valueType, valueType)
	if err != nil {
		return "", err
	}
	nullable := node.Nullable && strings.HasPrefix(field.Type, "*")
	fmt.Fprintf(&e.helpers, "\ntype %s struct { Value %s; Present bool", name, valueType)
	if nullable {
		e.helpers.WriteString("; Null bool")
	}
	fmt.Fprintf(&e.helpers, " }\nfunc (v *%s) UnmarshalJSON(data []byte) error {\nif v.Present { return fmt.Errorf(%q) }\nv.Present = true\n", name, field.JSONName+": duplicate annotated JSON field")
	if nullable {
		e.helpers.WriteString("if bytes.Equal(data, []byte(\"null\")) { v.Null = true; return nil }\n")
	}
	fmt.Fprintf(&e.helpers, "value, err := %s(data)\nif err != nil { return fmt.Errorf(%q, err) }\nv.Value = value\nreturn nil\n}\n", decoder, field.JSONName+": %w")
	return name, nil
}

func (e *emitter) decoder(node *rule, goType, underlying string) (string, error) {
	name := e.symbol()
	var out strings.Builder
	fmt.Fprintf(&out, "\nfunc %s(data []byte) (%s, error) {\nvar value %s\ndata = bytes.TrimSpace(data)\n", name, goType, goType)
	out.WriteString("if len(data) == 0 { return value, fmt.Errorf(\"missing JSON value\") }\nif bytes.Equal(data, []byte(\"null\")) {\n")
	if node.Nullable && (!node.isScalar() || node.Target.Kind == "custom") {
		if node.isScalar() {
			if node.Delegate {
				out.WriteString("err := json.Unmarshal(data, &value)\nreturn value, err\n")
			} else {
				e.customConversion(&out, node.Target)
			}
		} else {
			out.WriteString("return value, nil\n")
		}
	} else {
		out.WriteString("return value, fmt.Errorf(\"null is not allowed\")\n")
	}
	out.WriteString("}\n")
	switch {
	case node.Delegate:
		out.WriteString("err := json.Unmarshal(data, &value)\nreturn value, err\n")
	case node.isScalar():
		if err := e.scalarDecoder(&out, node, goType); err != nil {
			return "", err
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
	case node.Kind == "map":
		if !strings.HasPrefix(underlying, "map[string]") {
			return "", fmt.Errorf("%s: expected a string-keyed Go map, got %s", e.model.Name, underlying)
		}
		itemType := strings.TrimPrefix(underlying, "map[string]")
		decoder, err := e.decoder(node.Items, itemType, itemType)
		if err != nil {
			return "", err
		}
		out.WriteString("if data[0] != '{' { return value, fmt.Errorf(\"expected object\") }\nreader := json.NewDecoder(bytes.NewReader(data))\nif _, err := reader.Token(); err != nil { return value, err }\n")
		fmt.Fprintf(&out, "value = make(%s)\nfor reader.More() {\ntoken, err := reader.Token()\nif err != nil { return nil, err }\nkey := token.(string)\nif _, exists := value[key]; exists { return nil, fmt.Errorf(\"duplicate dictionary key %%q\", key) }\nvar raw json.RawMessage\nif err := reader.Decode(&raw); err != nil { return nil, err }\nitem, err := %s(raw)\nif err != nil { return nil, fmt.Errorf(\"key %%q: %%w\", key, err) }\nvalue[key] = item\n}\n_, err := reader.Token()\nreturn value, err\n", goType, decoder)
	default:
		return "", fmt.Errorf("unsupported decoder kind %q", node.Kind)
	}
	out.WriteString("}\n")
	e.helpers.WriteString(out.String())
	return name, nil
}

func (e *emitter) fieldConversion(out *strings.Builder, child *rule, field Field, raw string) error {
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
		if strings.HasPrefix(field.Type, "*") {
			fmt.Fprintf(out, "next.%s = &value\n", field.GoName)
		} else {
			fmt.Fprintf(out, "next.%s = value\n", field.GoName)
		}
	}
	return nil
}

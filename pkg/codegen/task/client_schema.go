package codegen

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

// Client schemas use JSON wire types. Go-specific type/import extensions do not
// apply, and resolving a reference must never silently degrade it to Any.
type clientTypes struct {
	language    string
	definitions []string
	documents   map[string]any
	activeRefs  map[string]bool
	names       map[string]int
}

func newClientTypes(language string) *clientTypes {
	return &clientTypes{language: language, documents: map[string]any{}, activeRefs: map[string]bool{}, names: map[string]int{}}
}

func (g *clientTypes) alias(name, typ string) {
	if g.language == "python" {
		g.definitions = append(g.definitions, name+" = "+typ)
	} else {
		g.definitions = append(g.definitions, "export type "+name+" = "+typ+";")
	}
}

func (g *clientTypes) reference(file, ref string) (string, map[string]any, string, error) {
	u, err := url.Parse(strings.Replace(ref, "#schemas/", "#/schemas/", 1))
	if err != nil || u.IsAbs() || u.Host != "" || u.RawQuery != "" {
		return "", nil, "", fmt.Errorf("only local schema references are supported: %q", ref)
	}
	target := file
	if u.Path != "" {
		target = filepath.Clean(filepath.Join(filepath.Dir(file), filepath.FromSlash(u.Path)))
	}
	doc, ok := g.documents[target]
	if !ok {
		raw, err := os.ReadFile(target)
		if err != nil {
			return "", nil, "", fmt.Errorf("resolve %s: %w", ref, err)
		}
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return "", nil, "", err
		}
		g.documents[target] = doc
	}
	if u.Fragment != "" {
		if !strings.HasPrefix(u.Fragment, "/") {
			return "", nil, "", fmt.Errorf("schema reference must use a JSON pointer: %s", ref)
		}
		for _, key := range strings.Split(u.Fragment[1:], "/") {
			key = strings.ReplaceAll(strings.ReplaceAll(key, "~1", "/"), "~0", "~")
			obj, ok := doc.(map[string]any)
			if !ok {
				return "", nil, "", fmt.Errorf("schema reference not found: %s", ref)
			}
			doc, ok = obj[key]
			if !ok {
				return "", nil, "", fmt.Errorf("schema reference not found: %s", ref)
			}
		}
	}
	schema, ok := doc.(map[string]any)
	if !ok {
		return "", nil, "", fmt.Errorf("schema reference must point to an object: %s", ref)
	}
	return target, schema, target + "#" + u.Fragment, nil
}

func (g *clientTypes) render(file, name string, schema map[string]any) (string, error) {
	if ref, ok := schema["$ref"].(string); ok {
		target, resolved, key, err := g.reference(file, ref)
		if err != nil {
			return "", err
		}
		if g.activeRefs[key] {
			return "", fmt.Errorf("recursive schema reference is not supported: %s", ref)
		}
		g.activeRefs[key] = true
		defer delete(g.activeRefs, key)
		// Preserve nullable siblings used by OpenAPI task schemas.
		if schema["nullable"] == true {
			resolved = copySchema(resolved)
			resolved["nullable"] = true
		}
		return g.render(target, name, resolved)
	}
	typ, err := g.renderValue(file, name, schema)
	if err != nil {
		return "", err
	}
	if schema["nullable"] == true {
		if g.language == "python" {
			typ = "Union[" + typ + ", None]"
		} else {
			typ = "(" + typ + " | null)"
		}
	}
	return typ, nil
}

func (g *clientTypes) renderValue(file, name string, schema map[string]any) (string, error) {
	if values, ok := schema["enum"].([]any); ok && len(values) > 0 {
		var literals []string
		for _, value := range values {
			switch value.(type) {
			case string, int, int64, bool, nil:
			default:
				return "", fmt.Errorf("%s: enum values must be strings, integers, booleans or null", name)
			}
			literals = append(literals, clientLiteral(value, g.language))
		}
		if g.language == "python" {
			return "Literal[" + strings.Join(literals, ", ") + "]", nil
		}
		return "(" + strings.Join(literals, " | ") + ")", nil
	}
	for _, keyword := range []string{"oneOf", "anyOf"} {
		if branches, ok := schema[keyword].([]any); ok {
			var types []string
			for i, raw := range branches {
				branch, ok := raw.(map[string]any)
				if !ok {
					return "", fmt.Errorf("%s must contain schema objects", keyword)
				}
				typ, err := g.render(file, fmt.Sprintf("%sVariant%d", name, i+1), branch)
				if err != nil {
					return "", err
				}
				types = append(types, typ)
			}
			if len(types) == 0 {
				return "", fmt.Errorf("%s must not be empty", keyword)
			}
			if g.language == "python" {
				return "Union[" + strings.Join(types, ", ") + "]", nil
			}
			return "(" + strings.Join(types, " | ") + ")", nil
		}
	}
	if _, ok := schema["allOf"]; ok {
		return "", fmt.Errorf("%s: allOf is not supported in task clients; use an object or a schema reference", name)
	}
	if union, ok := schema["type"].([]any); ok {
		branches := make([]any, 0, len(union))
		for _, typ := range union {
			branch := copySchema(schema)
			branch["type"] = typ
			branches = append(branches, branch)
		}
		return g.renderValue(file, name, map[string]any{"anyOf": branches})
	}
	typ, _ := schema["type"].(string)
	if typ == "" && (schema["properties"] != nil || schema["additionalProperties"] != nil) {
		typ = "object"
	}
	python := g.language == "python"
	switch typ {
	case "string":
		if python {
			return "str", nil
		}
		return "string", nil
	case "integer", "number":
		if python {
			if typ == "integer" {
				return "int", nil
			}
			return "float", nil
		}
		return "number", nil
	case "boolean":
		if python {
			return "bool", nil
		}
		return "boolean", nil
	case "null":
		if python {
			return "None", nil
		}
		return "null", nil
	case "array":
		items, _ := schema["items"].(map[string]any)
		itemType, err := g.render(file, name+"Item", items)
		if err != nil {
			return "", err
		}
		if python {
			return "list[" + itemType + "]", nil
		}
		return "Array<" + itemType + ">", nil
	case "object":
		return g.object(file, name, schema)
	case "":
		if python {
			return "Any", nil
		}
		return "unknown", nil
	default:
		return "", fmt.Errorf("%s: unsupported schema type %q", name, typ)
	}
}

func (g *clientTypes) object(file, name string, schema map[string]any) (string, error) {
	properties, _ := schema["properties"].(map[string]any)
	python := g.language == "python"
	if len(properties) == 0 {
		additional, _ := schema["additionalProperties"].(map[string]any)
		value, err := g.render(file, name+"Value", additional)
		if python {
			return "dict[str, " + value + "]", err
		}
		return "Record<string, " + value + ">", err
	}
	name = clientTypeName(name)
	base := name
	for suffix := 2; g.names[name] != 0; suffix++ {
		name = fmt.Sprintf("%s%d", base, suffix)
	}
	g.names[name] = 1
	required := map[string]bool{}
	if fields, ok := schema["required"].([]any); ok {
		for _, field := range fields {
			if field, ok := field.(string); ok {
				required[field] = true
			}
		}
	}
	keys := make([]string, 0, len(properties))
	for key := range properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var def strings.Builder
	if python {
		fmt.Fprintf(&def, "%s = TypedDict(%q, {\n", name, name)
	} else {
		fmt.Fprintf(&def, "export type %s = {\n", name)
	}
	for _, key := range keys {
		property, ok := properties[key].(map[string]any)
		if !ok {
			return "", fmt.Errorf("property %s must be a schema object", key)
		}
		typ, err := g.render(file, name+clientTypeName(key), property)
		if err != nil {
			return "", err
		}
		if python {
			wrapper := "NotRequired"
			if required[key] {
				wrapper = "Required"
			}
			fmt.Fprintf(&def, "    %s: %s[%s],\n", clientLiteral(key, g.language), wrapper, typ)
		} else {
			optional := "?"
			if required[key] {
				optional = ""
			}
			fmt.Fprintf(&def, "  %s%s: %s;\n", clientLiteral(key, g.language), optional, typ)
		}
	}
	if python {
		def.WriteString("})")
	} else {
		def.WriteString("};")
	}
	g.definitions = append(g.definitions, def.String())
	return name, nil
}

func copySchema(schema map[string]any) map[string]any {
	copy := make(map[string]any, len(schema))
	for key, value := range schema {
		copy[key] = value
	}
	return copy
}

func clientTypeName(name string) string {
	var b strings.Builder
	upper := true
	for _, r := range name {
		if r > unicode.MaxASCII || (!unicode.IsLetter(r) && !unicode.IsDigit(r)) {
			upper = true
			continue
		}
		if b.Len() == 0 && unicode.IsDigit(r) {
			b.WriteByte('T')
		}
		if upper {
			r = unicode.ToUpper(r)
		}
		b.WriteRune(r)
		upper = false
	}
	if b.Len() == 0 {
		return "Value"
	}
	return b.String()
}

func clientLiteral(value any, language string) string {
	// JSON round-tripping normalizes typed slices and integers before Python
	// literal rendering. User strings are quoted as data, never source code.
	raw, _ := json.Marshal(value)
	if language != "python" {
		return string(raw)
	}
	var normalized any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	_ = decoder.Decode(&normalized)
	return pythonLiteral(normalized)
}

func pythonLiteral(value any) string {
	switch value := value.(type) {
	case nil:
		return "None"
	case bool:
		if value {
			return "True"
		}
		return "False"
	case []any:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			parts = append(parts, pythonLiteral(item))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, pythonLiteral(key)+": "+pythonLiteral(value[key]))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	default:
		raw, _ := json.Marshal(value)
		return string(raw)
	}
}

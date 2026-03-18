package schemas

import (
	"encoding/json"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/cloudcarver/anclax/pkg/codegen/gotypes"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Path   string
	Output string
}

type Manager struct {
	workdir    string
	modulePath string
	rootPath   string
	outputPath string
	files      map[string]*File
}

type File struct {
	AbsPath    string
	RelPath    string
	Package    string
	ImportPath string
	Schemas    map[string]*openapi3.SchemaRef
}

type schemaDoc struct {
	Schemas map[string]*openapi3.SchemaRef
}

type enumDef struct {
	Name   string
	Type   string
	Values []enumValue
}

type enumValue struct {
	Name    string
	Literal string
}

type schemaDef struct {
	Name      string
	Kind      string
	AliasType string
	Alias     bool
	Fields    []fieldDef
}

type fieldDef struct {
	Name        string
	JSONName    string
	Type        string
	Description string
	Optional    bool
}

type resolvedType struct {
	GoType  string
	Imports []string
}

func Load(workdir string, config Config) (*Manager, error) {
	if config.Path == "" || config.Output == "" {
		return nil, nil
	}
	modulePath, err := parseModulePath(filepath.Join(workdir, "go.mod"))
	if err != nil {
		return nil, err
	}
	rootPath := config.Path
	if !filepath.IsAbs(rootPath) {
		rootPath = filepath.Join(workdir, rootPath)
	}
	outputPath := config.Output
	if !filepath.IsAbs(outputPath) {
		outputPath = filepath.Join(workdir, outputPath)
	}
	m := &Manager{
		workdir:    workdir,
		modulePath: modulePath,
		rootPath:   filepath.Clean(rootPath),
		outputPath: filepath.Clean(outputPath),
		files:      map[string]*File{},
	}
	if _, err := os.Stat(m.rootPath); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if err := filepath.Walk(m.rootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}
		file, err := loadFile(m, path)
		if err != nil {
			return err
		}
		m.files[file.AbsPath] = file
		return nil
	}); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) IsEnabled() bool {
	return m != nil
}

func (m *Manager) ResolveRef(currentFile, currentPackage, ref string) (string, []string, bool, error) {
	if m == nil || ref == "" {
		return "", nil, false, nil
	}
	ref = normalizeRefString(ref)
	parts := strings.SplitN(ref, "#", 2)
	refPath := parts[0]
	fragment := ""
	if len(parts) == 2 {
		fragment = parts[1]
	}
	targetSchema, ok := schemaNameFromFragment(fragment)
	if !ok {
		return "", nil, false, nil
	}
	targetFile := currentFile
	if refPath != "" {
		baseDir := filepath.Dir(currentFile)
		targetFile = filepath.Clean(filepath.Join(baseDir, filepath.FromSlash(refPath)))
	}
	if !strings.HasPrefix(targetFile, m.rootPath) {
		return "", nil, false, nil
	}
	file, ok := m.files[targetFile]
	if !ok {
		return "", nil, false, errors.Errorf("schema ref target not found: %s", ref)
	}
	if _, ok := file.Schemas[targetSchema]; !ok {
		return "", nil, false, errors.Errorf("schema %s not found in %s", targetSchema, ref)
	}
	goName := exportName(targetSchema)
	if currentPackage == file.Package {
		return goName, nil, true, nil
	}
	return file.Package + "." + goName, []string{file.ImportPath}, true, nil
}

func (m *Manager) Generate() error {
	if m == nil {
		return nil
	}
	if err := os.RemoveAll(m.outputPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	if len(m.files) == 0 {
		return nil
	}
	files := make([]*File, 0, len(m.files))
	for _, file := range m.files {
		files = append(files, file)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].RelPath < files[j].RelPath })
	for _, file := range files {
		code, err := m.renderFile(file)
		if err != nil {
			return errors.Wrapf(err, "failed to render schema file %s", file.RelPath)
		}
		outPath := filepath.Join(m.outputPath, strings.TrimSuffix(file.RelPath, filepath.Ext(file.RelPath))+".go")
		if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(outPath, []byte(code), 0644); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) renderFile(file *File) (string, error) {
	enumMap := map[string]*enumDef{}
	imports := map[string]struct{}{}
	schemaNames := make([]string, 0, len(file.Schemas))
	for name := range file.Schemas {
		schemaNames = append(schemaNames, name)
	}
	sort.Strings(schemaNames)
	defs := make([]schemaDef, 0, len(schemaNames))
	for _, name := range schemaNames {
		def, err := m.buildSchemaDef(file, name, file.Schemas[name], enumMap, imports)
		if err != nil {
			return "", err
		}
		defs = append(defs, def)
	}
	enums := make([]enumDef, 0, len(enumMap))
	for _, enum := range enumMap {
		enums = append(enums, *enum)
	}
	sort.Slice(enums, func(i, j int) bool { return enums[i].Name < enums[j].Name })

	var b strings.Builder
	b.WriteString("// Code generated by github.com/cloudcarver/anclax DO NOT EDIT.\n")
	b.WriteString("package ")
	b.WriteString(file.Package)
	b.WriteString("\n\n")
	renderImports(&b, sortedImports(imports))
	for _, enum := range enums {
		b.WriteString("const (\n")
		for _, value := range enum.Values {
			b.WriteString("\t")
			b.WriteString(value.Name)
			b.WriteString(" ")
			b.WriteString(enum.Name)
			b.WriteString(" = ")
			b.WriteString(value.Literal)
			b.WriteString("\n")
		}
		b.WriteString(")\n\n")
	}
	for _, def := range defs {
		if def.Kind == "alias" {
			if def.AliasType == def.Name {
				continue
			}
			if def.Alias {
				b.WriteString("type ")
				b.WriteString(def.Name)
				b.WriteString(" = ")
				b.WriteString(def.AliasType)
				b.WriteString("\n\n")
			} else {
				b.WriteString("type ")
				b.WriteString(def.Name)
				b.WriteString(" ")
				b.WriteString(def.AliasType)
				b.WriteString("\n\n")
			}
			continue
		}
		b.WriteString("type ")
		b.WriteString(def.Name)
		b.WriteString(" struct {\n")
		for _, field := range def.Fields {
			writeComment(&b, field.Description, "\t")
			b.WriteString("\t")
			b.WriteString(field.Name)
			b.WriteString(" ")
			b.WriteString(field.Type)
			b.WriteString(" `json:")
			b.WriteString(strconv.Quote(jsonTag(field.JSONName, field.Optional)))
			b.WriteString(" yaml:")
			b.WriteString(strconv.Quote(jsonTag(field.JSONName, field.Optional)))
			b.WriteString("`\n")
		}
		b.WriteString("}\n\n")
	}
	for _, enum := range enums {
		b.WriteString("type ")
		b.WriteString(enum.Name)
		b.WriteString(" ")
		b.WriteString(enum.Type)
		b.WriteString("\n\n")
	}
	formatted, err := format.Source([]byte(b.String()))
	if err != nil {
		return "", err
	}
	return string(formatted), nil
}

func (m *Manager) buildSchemaDef(file *File, name string, ref *openapi3.SchemaRef, enumMap map[string]*enumDef, imports map[string]struct{}) (schemaDef, error) {
	def := schemaDef{Name: exportName(name)}
	if ref == nil {
		return def, nil
	}
	if ref.Ref != "" {
		if goType, imp, ok, err := m.ResolveRef(file.AbsPath, file.Package, ref.Ref); err != nil {
			return def, err
		} else if ok {
			mergeImports(imports, imp...)
			def.Kind = "alias"
			def.AliasType = goType
			def.Alias = true
			return def, nil
		}
	}
	resolved, err := m.resolveType(file, ref, def.Name, enumMap)
	if err != nil {
		return def, err
	}
	mergeImports(imports, resolved.Imports...)
	value := ref.Value
	if value != nil && ((value.Type != nil && value.Type.Is("object")) || len(value.Properties) > 0) {
		def.Kind = "object"
		required := requiredSet(value.Required)
		propNames := make([]string, 0, len(value.Properties))
		for prop := range value.Properties {
			propNames = append(propNames, prop)
		}
		sort.Strings(propNames)
		for _, propName := range propNames {
			propRef := value.Properties[propName]
			propResolved, err := m.resolveType(file, propRef, def.Name+exportName(propName), enumMap)
			if err != nil {
				return def, err
			}
			mergeImports(imports, propResolved.Imports...)
			fieldType := propResolved.GoType
			optional := !required[propName]
			if optional {
				fieldType = pointerType(fieldType)
			}
			description := ""
			if propRef != nil && propRef.Value != nil {
				description = propRef.Value.Description
			}
			def.Fields = append(def.Fields, fieldDef{
				Name:        exportName(propName),
				JSONName:    propName,
				Type:        fieldType,
				Description: description,
				Optional:    optional,
			})
		}
		return def, nil
	}
	def.Kind = "alias"
	def.AliasType = resolved.GoType
	def.Alias = true
	return def, nil
}

func (m *Manager) resolveType(file *File, ref *openapi3.SchemaRef, hint string, enumMap map[string]*enumDef) (resolvedType, error) {
	if ref == nil {
		return resolvedType{GoType: "interface{}"}, nil
	}
	if ref.Ref != "" {
		if goType, imp, ok, err := m.ResolveRef(file.AbsPath, file.Package, ref.Ref); err != nil {
			return resolvedType{}, err
		} else if ok {
			return resolvedType{GoType: goType, Imports: imp}, nil
		}
	}
	if ref.Value == nil {
		return resolvedType{GoType: "interface{}"}, nil
	}
	if goType, imports := customGoType(ref.Value); goType != "" {
		return resolvedType{GoType: goType, Imports: imports}, nil
	}
	if len(ref.Value.Enum) > 0 {
		name := exportName(hint)
		baseType := primitiveType(ref.Value)
		ensureEnum(enumMap, name, baseType, ref.Value.Enum)
		return resolvedType{GoType: name}, nil
	}
	if ref.Value.Type == nil {
		return resolvedType{GoType: "interface{}"}, nil
	}
	if goType, imports, ok := gotypes.ResolvePrimitive(ref.Value); ok {
		return resolvedType{GoType: goType, Imports: imports}, nil
	}
	switch {
	case ref.Value.Type.Is("array"):
		item, err := m.resolveType(file, ref.Value.Items, hint+"Item", enumMap)
		if err != nil {
			return resolvedType{}, err
		}
		return resolvedType{GoType: "[]" + item.GoType, Imports: item.Imports}, nil
	case ref.Value.Type.Is("object"):
		return resolvedType{GoType: "map[string]any"}, nil
	default:
		return resolvedType{GoType: "interface{}"}, nil
	}
}

func loadFile(m *Manager, path string) (*File, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	raw = NormalizeRefBytes(raw)
	doc, err := parseSchemaDoc(raw)
	if err != nil {
		return nil, err
	}
	relPath, err := filepath.Rel(m.rootPath, path)
	if err != nil {
		return nil, err
	}
	relDir := filepath.Dir(relPath)
	pkg := "schemas"
	if relDir != "." {
		pkg = filepath.Base(relDir)
	}
	outRelDir, err := filepath.Rel(m.workdir, filepath.Join(m.outputPath, relDir))
	if err != nil {
		return nil, err
	}
	importPath := pathJoinSlash(m.modulePath, outRelDir)
	if doc.Schemas == nil {
		doc.Schemas = map[string]*openapi3.SchemaRef{}
	}
	return &File{AbsPath: filepath.Clean(path), RelPath: relPath, Package: pkg, ImportPath: importPath, Schemas: doc.Schemas}, nil
}

func parseSchemaDoc(raw []byte) (schemaDoc, error) {
	var data map[string]any
	if err := yaml.Unmarshal(raw, &data); err != nil {
		return schemaDoc{}, err
	}
	doc := schemaDoc{Schemas: map[string]*openapi3.SchemaRef{}}
	rawSchemas, ok := data["schemas"]
	if !ok {
		return doc, nil
	}
	schemasMap, ok := rawSchemas.(map[string]any)
	if !ok {
		return doc, errors.New("schemas must be a map")
	}
	for name, rawSchema := range schemasMap {
		ref, err := UnmarshalSchemaRef(rawSchema)
		if err != nil {
			return doc, errors.Wrapf(err, "failed to parse schema %s", name)
		}
		doc.Schemas[name] = ref
	}
	return doc, nil
}

func parseModulePath(goModPath string) (string, error) {
	data, err := os.ReadFile(goModPath)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module ")), nil
		}
	}
	return "", errors.New("module path not found in go.mod")
}

func NormalizeRefBytes(raw []byte) []byte {
	re := regexp.MustCompile(`(#)schemas/`)
	return re.ReplaceAll(raw, []byte(`${1}/schemas/`))
}

func normalizeRefString(ref string) string {
	return strings.Replace(ref, "#schemas/", "#/schemas/", 1)
}

func schemaNameFromFragment(fragment string) (string, bool) {
	fragment = strings.TrimPrefix(fragment, "/")
	parts := strings.Split(fragment, "/")
	if len(parts) != 2 || parts[0] != "schemas" || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

func ensureEnum(enumMap map[string]*enumDef, name, goType string, values []any) {
	if _, ok := enumMap[name]; ok {
		return
	}
	e := &enumDef{Name: name, Type: goType}
	for _, value := range values {
		e.Values = append(e.Values, enumValue{Name: exportName(fmt.Sprint(value)), Literal: enumLiteral(goType, value)})
	}
	enumMap[name] = e
}

func customGoType(schema *openapi3.Schema) (string, []string) {
	if schema == nil || schema.Extensions == nil {
		return "", nil
	}
	val, ok := schema.Extensions["x-go-type"]
	if !ok {
		return "", nil
	}
	goType, ok := val.(string)
	if !ok {
		return "", nil
	}
	var imports []string
	if rawImports, ok := schema.Extensions["x-go-type-imports"]; ok {
		switch typed := rawImports.(type) {
		case []any:
			for _, item := range typed {
				if str, ok := item.(string); ok {
					imports = append(imports, str)
				}
			}
		}
	}
	return goType, imports
}

func primitiveType(schema *openapi3.Schema) string {
	return gotypes.Primitive(schema)
}

func renderImports(b *strings.Builder, imports []string) {
	if len(imports) == 0 {
		return
	}
	if len(imports) == 1 {
		b.WriteString("import ")
		b.WriteString(strconv.Quote(imports[0]))
		b.WriteString("\n\n")
		return
	}
	b.WriteString("import (\n")
	for _, imp := range imports {
		b.WriteString("\t")
		b.WriteString(strconv.Quote(imp))
		b.WriteString("\n")
	}
	b.WriteString(")\n\n")
}

func sortedImports(imports map[string]struct{}) []string {
	ret := make([]string, 0, len(imports))
	for imp := range imports {
		ret = append(ret, imp)
	}
	sort.Strings(ret)
	return ret
}

func mergeImports(target map[string]struct{}, imports ...string) {
	for _, imp := range imports {
		if imp == "" {
			continue
		}
		target[imp] = struct{}{}
	}
}

func requiredSet(items []string) map[string]bool {
	ret := map[string]bool{}
	for _, item := range items {
		ret[item] = true
	}
	return ret
}

func pointerType(t string) string {
	if strings.HasPrefix(t, "*") || strings.HasPrefix(t, "[]") || strings.HasPrefix(t, "map[") {
		return "*" + t
	}
	return "*" + t
}

func jsonTag(name string, optional bool) string {
	if optional {
		return name + ",omitempty"
	}
	return name
}

func writeComment(b *strings.Builder, text, indent string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		b.WriteString(indent)
		b.WriteString("// ")
		b.WriteString(line)
		b.WriteString("\n")
	}
}

func enumLiteral(goType string, value any) string {
	if goType == "string" {
		return strconv.Quote(fmt.Sprint(value))
	}
	return fmt.Sprint(value)
}

var wordBoundary = regexp.MustCompile(`[^a-zA-Z0-9]+`)

func exportName(value string) string {
	parts := splitWords(value)
	for i, part := range parts {
		if part == strings.ToUpper(part) {
			parts[i] = part
		} else {
			parts[i] = strings.ToUpper(part[:1]) + part[1:]
		}
	}
	return strings.Join(parts, "")
}

func splitWords(value string) []string {
	value = wordBoundary.ReplaceAllString(value, " ")
	parts := strings.Fields(value)
	var ret []string
	for _, part := range parts {
		runes := []rune(part)
		start := 0
		for i := 1; i < len(runes); i++ {
			prev, curr := runes[i-1], runes[i]
			nextLower := i+1 < len(runes) && runes[i+1] >= 'a' && runes[i+1] <= 'z'
			if curr >= 'A' && curr <= 'Z' && ((prev >= 'a' && prev <= 'z') || ((prev >= 'A' && prev <= 'Z') && nextLower)) {
				ret = append(ret, string(runes[start:i]))
				start = i
			}
		}
		ret = append(ret, string(runes[start:]))
	}
	return ret
}

func pathJoinSlash(parts ...string) string {
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		clean = append(clean, strings.Trim(filepath.ToSlash(part), "/"))
	}
	return strings.Join(clean, "/")
}

func MarshalNormalized(v any) ([]byte, error) {
	buf, err := yaml.Marshal(v)
	if err != nil {
		return nil, err
	}
	return NormalizeRefBytes(buf), nil
}

func UnmarshalSchemaRef(raw any) (*openapi3.SchemaRef, error) {
	buf, err := MarshalNormalized(raw)
	if err != nil {
		return nil, err
	}
	var rawMap map[string]any
	if err := yaml.Unmarshal(buf, &rawMap); err != nil {
		return nil, err
	}
	if refValue, ok := rawMap["$ref"].(string); ok {
		return &openapi3.SchemaRef{Ref: refValue}, nil
	}
	jsonBuf, err := json.Marshal(rawMap)
	if err != nil {
		return nil, err
	}
	var schema openapi3.Schema
	if err := json.Unmarshal(jsonBuf, &schema); err != nil {
		return nil, err
	}
	return &openapi3.SchemaRef{Value: &schema}, nil
}

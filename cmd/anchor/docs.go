package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
)

var docsCmd = &cli.Command{
	Name: "docs",
	Subcommands: []*cli.Command{
		{
			Name: "config",
			Flags: []cli.Flag{
				&cli.StringFlag{
					Name:  "path",
					Usage: "path to the file to parse",
					Value: "",
				},
				&cli.BoolFlag{
					Name:  "markdown",
					Usage: "output in markdown format",
				},
				&cli.BoolFlag{
					Name:  "env",
					Usage: "output environment variables",
				},
				&cli.BoolFlag{
					Name:  "yaml",
					Usage: "output yaml sample",
				},
				&cli.StringFlag{
					Name:  "prefix",
					Usage: "prefix for environment variables",
					Value: "",
				},
				&cli.StringFlag{
					Name:  "struct",
					Usage: "name of the struct to parse",
					Value: "",
				},
			},
			Action: runGenConfigDocs,
		},
		{
			Name: "replace",
			Flags: []cli.Flag{
				&cli.StringFlag{
					Name:  "key",
					Usage: "key to use for the template",
					Value: "",
				},
				&cli.StringFlag{
					Name:  "file",
					Usage: "path to the file of content to use for the replacement",
					Value: "",
				},
				&cli.StringFlag{
					Name:  "value",
					Usage: "value to use for the replacement",
					Value: "",
				},
			},
			Action: runGenWithTemplate,
		},
	},
}

func runGenWithTemplate(c *cli.Context) error {
	key := c.String("key")
	file := c.String("file")
	content := c.String("content")

	template, err := io.ReadAll(os.Stdin)
	if err != nil {
		return errors.Wrap(err, "failed to read from stdin")
	}

	if file != "" && content != "" {
		return errors.New("file and content flags cannot be used together")
	}

	if file == "" && content == "" {
		return errors.New("one of file or content flag is required")
	}

	if key == "" {
		return errors.New("key is required")
	}

	textToReplace := fmt.Sprintf("{{%s}}", key)

	if file != "" {
		content, err := os.ReadFile(file)
		if err != nil {
			return errors.Wrap(err, "failed to read file")
		}
		fmt.Println(strings.ReplaceAll(string(template), textToReplace, string(content)))
	} else {
		fmt.Println(strings.ReplaceAll(string(template), textToReplace, content))
	}
	return nil
}

func runGenConfigDocs(c *cli.Context) error {
	return genConfigDocs(c.String("path"), c.Bool("markdown"), c.Bool("env"), c.Bool("yaml"), c.String("prefix"), c.String("struct"))
}

func genConfigDocs(path string, markdown, env, yaml bool, prefix string, structName string) error {
	configStructName := "Config"
	if structName != "" {
		configStructName = structName
	}

	if path == "" {
		return errors.New("path is required")
	}

	if yaml && env {
		return errors.New("yaml and env flags cannot be used together")
	}

	if !yaml && !env {
		env = true // default to env output
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return errors.Wrap(err, "failed to read directory")
	}

	// Parse all Go files in the directory
	fset := token.NewFileSet()
	var files []*ast.File
	var configStruct *ast.StructType
	var imports map[string]string                  // alias -> package path
	localTypes := make(map[string]*ast.StructType) // type name -> struct definition

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}

		filePath := filepath.Join(path, entry.Name())
		node, err := parser.ParseFile(fset, filePath, nil, parser.ParseComments)
		if err != nil {
			continue
		}
		files = append(files, node)

		// Extract imports for this file
		if imports == nil {
			imports = make(map[string]string)
		}
		for _, imp := range node.Imports {
			path := strings.Trim(imp.Path.Value, "\"")
			if imp.Name != nil {
				// Aliased import: import alias "package"
				imports[imp.Name.Name] = path
			} else {
				// Regular import: determine the actual package name
				var pkgName string
				if strings.HasSuffix(path, "/v2") || strings.HasSuffix(path, "/v3") {
					// For versioned packages like github.com/urfave/cli/v2,
					// the package name is the second-to-last path segment
					parts := strings.Split(path, "/")
					if len(parts) >= 2 {
						pkgName = parts[len(parts)-2]
					} else {
						pkgName = parts[len(parts)-1]
					}
				} else {
					// For regular packages, use the last path segment
					parts := strings.Split(path, "/")
					pkgName = parts[len(parts)-1]
				}
				imports[pkgName] = path
			}
		}

		// Look for all struct definitions and the config struct
		ast.Inspect(node, func(n ast.Node) bool {
			if ts, ok := n.(*ast.TypeSpec); ok {
				if st, ok := ts.Type.(*ast.StructType); ok {
					// Store all struct types for local resolution
					localTypes[ts.Name.Name] = st

					// Check if this is our target config struct
					if ts.Name.Name == configStructName {
						configStruct = st
					}
				}
			}
			return true
		})
	}

	if configStruct == nil {
		return errors.New("Config struct not found")
	}

	// Note: We no longer need local type checking since we use go/packages for external types

	vars := make([]EnvVar, 0)
	typeResolver := &TypeResolver{
		fset:       fset,
		imports:    imports,
		localTypes: localTypes,
	}
	for _, field := range configStruct.Fields.List {
		processFieldWithResolver(field, nil, &vars, typeResolver)
	}

	if yaml {
		printYAMLSample(prefix, vars)
	} else if env {
		if markdown {
			printEnvMarkdown(prefix, vars)
		} else {
			printEnvText(prefix, vars)
		}
	}
	return nil
}

// TypeResolver helps resolve external types using dynamic package loading
type TypeResolver struct {
	fset       *token.FileSet
	imports    map[string]string          // alias -> package path
	localTypes map[string]*ast.StructType // local type name -> struct definition
}

// findPackageSourcePath finds the source directory for a package using go list
func (tr *TypeResolver) findPackageSourcePath(packagePath string) (string, error) {
	cmd := exec.Command("go", "list", "-f", "{{.Dir}}", packagePath)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to find package %s: %v", packagePath, err)
	}

	return strings.TrimSpace(string(output)), nil
}

// expandExternalType dynamically resolves external struct fields by parsing source
func (tr *TypeResolver) expandExternalType(typeStr string) []Field {
	// Remove pointer prefix
	baseType := strings.TrimPrefix(typeStr, "*")
	parts := strings.Split(baseType, ".")
	if len(parts) != 2 {
		return nil
	}

	pkgAlias, typeName := parts[0], parts[1]

	// Get the actual package path
	pkgPath, exists := tr.imports[pkgAlias]
	if !exists {
		return nil
	}

	// Find the package source directory
	sourceDir, err := tr.findPackageSourcePath(pkgPath)
	if err != nil {
		return nil
	}

	// Parse the package source files
	return tr.parsePackageForType(sourceDir, typeName)
}

// parsePackageForType parses Go source files in a directory to find a specific type
func (tr *TypeResolver) parsePackageForType(sourceDir, typeName string) []Field {
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		return nil
	}

	// Parse all Go files in the package to build local type map
	packageLocalTypes := make(map[string]*ast.StructType)

	// First pass: collect all struct types in the package
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}

		filePath := filepath.Join(sourceDir, entry.Name())
		file, err := parser.ParseFile(token.NewFileSet(), filePath, nil, parser.ParseComments)
		if err != nil {
			continue
		}

		// Collect all struct types in this package
		ast.Inspect(file, func(n ast.Node) bool {
			if ts, ok := n.(*ast.TypeSpec); ok {
				if st, ok := ts.Type.(*ast.StructType); ok {
					packageLocalTypes[ts.Name.Name] = st
				}
			}
			return true
		})
	}

	// Second pass: find the target struct and extract its fields
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}

		filePath := filepath.Join(sourceDir, entry.Name())
		file, err := parser.ParseFile(token.NewFileSet(), filePath, nil, parser.ParseComments)
		if err != nil {
			continue
		}

		// Look for the struct definition
		var targetStruct *ast.StructType
		ast.Inspect(file, func(n ast.Node) bool {
			if ts, ok := n.(*ast.TypeSpec); ok {
				if st, ok := ts.Type.(*ast.StructType); ok && ts.Name.Name == typeName {
					targetStruct = st
					return false
				}
			}
			return true
		})

		if targetStruct != nil {
			// Extract fields from the struct
			return tr.extractFieldsFromASTStruct(targetStruct)
		}
	}

	return nil
}

// extractFieldsFromASTStruct extracts fields from an AST struct
func (tr *TypeResolver) extractFieldsFromASTStruct(structType *ast.StructType) []Field {
	var fields []Field

	for _, field := range structType.Fields.List {
		if field.Names == nil {
			continue // Skip embedded fields for now
		}

		for _, name := range field.Names {
			if !name.IsExported() {
				continue
			}

			// Get YAML field name
			var yamlName string
			if field.Tag != nil {
				yamlName = extractYAMLFieldName(field.Tag.Value, name.Name)
			} else {
				yamlName = strings.ToLower(name.Name)
			}

			if yamlName == "-" {
				continue
			}

			// Get field comment
			var comment string
			if field.Doc != nil {
				comments := make([]string, 0, len(field.Doc.List))
				for _, c := range field.Doc.List {
					comments = append(comments, strings.TrimSpace(strings.TrimPrefix(c.Text, "//")))
				}
				comment = strings.Join(comments, " ")
			}

			// Get the field type
			fieldType := getTypeString(field.Type)

			fields = append(fields, Field{
				Name:    yamlName,
				Type:    fieldType,
				Comment: comment,
			})
		}
	}

	return fields
}

// getKnownExternalTypeFields returns predefined field definitions for external types - REMOVE THIS
func (tr *TypeResolver) getKnownExternalTypeFields(pkgPath, typeName string) []Field {
	// Remove all hardcoded definitions - we want dynamic resolution only
	return nil
}

// extractYAMLFieldName extracts the YAML field name from struct tag
func extractYAMLFieldName(tag, defaultName string) string {
	if tag == "" {
		return strings.ToLower(defaultName)
	}

	// Parse yaml tag
	for _, part := range strings.Split(tag, " ") {
		part = strings.Trim(part, "`")
		if strings.HasPrefix(part, "yaml:") {
			yamlTag := strings.Trim(strings.TrimPrefix(part, "yaml:"), "\"")
			if yamlTag == "" {
				continue
			}
			// Split by comma and take the first part
			name := strings.Split(yamlTag, ",")[0]
			if name == "" {
				return strings.ToLower(defaultName)
			}
			return name
		}
	}

	return strings.ToLower(defaultName)
}

// shouldExpandExternalType determines if we should try to expand an external type
func (tr *TypeResolver) shouldExpandExternalType(typeStr string) bool {
	// Remove pointer prefix
	baseType := strings.TrimPrefix(typeStr, "*")
	parts := strings.Split(baseType, ".")
	if len(parts) != 2 {
		return false
	}

	pkgAlias, typeName := parts[0], parts[1]
	pkgPath, exists := tr.imports[pkgAlias]
	if !exists {
		return false
	}

	// Be more conservative - only expand types that look like struct types
	// and are likely to be configuration structures
	if strings.Contains(typeName, "Config") || strings.Contains(typeName, "Settings") ||
		strings.Contains(typeName, "Options") || strings.HasSuffix(typeName, "Spec") ||
		strings.HasSuffix(typeName, "Opts") || len(typeName) > 2 && strings.ToUpper(typeName[:1]) == typeName[:1] {
		// Try to find the package source to see if we can expand it
		if _, err := tr.findPackageSourcePath(pkgPath); err == nil {
			return true
		}
	}

	return false
}

// isPrimitiveOrKnownType returns true if the type is primitive or a known non-struct type
func isPrimitiveOrKnownType(typeStr string) bool {
	primitives := map[string]bool{
		"string":  true,
		"int":     true,
		"int8":    true,
		"int16":   true,
		"int32":   true,
		"int64":   true,
		"uint":    true,
		"uint8":   true,
		"uint16":  true,
		"uint32":  true,
		"uint64":  true,
		"bool":    true,
		"float32": true,
		"float64": true,
		"byte":    true,
		"rune":    true,
	}

	// Known non-struct types from common packages
	knownTypes := map[string]bool{
		"time.Time":       true,
		"time.Duration":   true,
		"url.URL":         true,
		"net.IP":          true,
		"json.RawMessage": true,
		"error":           true, // Built-in error interface
	}

	baseType := strings.TrimPrefix(typeStr, "*")
	return primitives[baseType] || knownTypes[baseType]
}

// Field represents a single field in the config structure
type Field struct {
	Name    string
	Type    string
	Comment string
}

// EnvVar represents an environment variable derived from a config field
type EnvVar struct {
	Chain []Field
}

func (e EnvVar) Path(prefix string) string {
	parts := make([]string, 0, len(e.Chain)+1)
	if prefix != "" {
		parts = append(parts, prefix)
	}
	for _, field := range e.Chain {
		parts = append(parts, field.Name)
	}
	return strings.ToUpper(strings.Join(parts, "_"))
}

func (e EnvVar) YAMLPath() string {
	parts := make([]string, len(e.Chain))
	for i, field := range e.Chain {
		parts[i] = field.Name
	}
	return strings.Join(parts, ".")
}

func (e EnvVar) LastField() Field {
	if len(e.Chain) == 0 {
		return Field{}
	}
	return e.Chain[len(e.Chain)-1]
}

// getTypeString returns a string representation of the type
func getTypeString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + getTypeString(t.X)
	case *ast.SelectorExpr:
		return fmt.Sprintf("%s.%s", t.X.(*ast.Ident).Name, t.Sel.Name)
	default:
		return fmt.Sprintf("%T", expr)
	}
}

// processStructFieldsWithResolver recursively processes struct fields with type resolution
func processStructFieldsWithResolver(field ast.Expr, chain []Field, vars *[]EnvVar, resolver *TypeResolver) {
	switch t := field.(type) {
	case *ast.Ident:
		typeStr := t.Name
		if isPrimitiveOrKnownType(typeStr) {
			*vars = append(*vars, EnvVar{Chain: chain})
		} else if localStruct, exists := resolver.localTypes[typeStr]; exists {
			// Resolve local struct type
			for _, f := range localStruct.Fields.List {
				processFieldWithResolver(f, chain, vars, resolver)
			}
		} else {
			// For unknown local types, treat as primitives
			*vars = append(*vars, EnvVar{Chain: chain})
		}
	case *ast.StarExpr:
		typeStr := getTypeString(t.X)
		if isPrimitiveOrKnownType(typeStr) {
			*vars = append(*vars, EnvVar{Chain: chain})
		} else {
			processStructFieldsWithResolver(t.X, chain, vars, resolver)
		}
	case *ast.SelectorExpr:
		typeStr := getTypeString(t)
		if isPrimitiveOrKnownType(typeStr) {
			*vars = append(*vars, EnvVar{Chain: chain})
		} else if resolver.shouldExpandExternalType(typeStr) {
			// Expand using dynamic struct resolution
			knownFields := resolver.expandExternalType(typeStr)
			if len(knownFields) > 0 {
				// Get the package path for potential nested type resolution
				parts := strings.Split(typeStr, ".")
				var pkgPath string
				if len(parts) == 2 {
					if path, exists := resolver.imports[parts[0]]; exists {
						pkgPath = path
					}
				}

				for _, knownField := range knownFields {
					newChain := make([]Field, len(chain))
					copy(newChain, chain)
					newChain = append(newChain, knownField)

					// Check if this field type should also be expanded (from the same package)
					fieldType := knownField.Type
					if !isPrimitiveOrKnownType(fieldType) && pkgPath != "" {
						// Create a SelectorExpr-like type for nested resolution
						if !strings.Contains(fieldType, ".") {
							// This is a local type in the same package
							nestedTypeStr := parts[0] + "." + strings.TrimPrefix(fieldType, "*")
							if resolver.shouldExpandExternalType(nestedTypeStr) {
								// Recursively expand this nested type
								nestedFields := resolver.expandExternalType(nestedTypeStr)
								if len(nestedFields) > 0 {
									for _, nestedField := range nestedFields {
										nestedChain := make([]Field, len(newChain))
										copy(nestedChain, newChain)
										nestedChain = append(nestedChain, nestedField)
										*vars = append(*vars, EnvVar{Chain: nestedChain})
									}
									continue // Skip adding the parent field as primitive
								}
							}
						}
					}

					// Add as primitive if not expandable
					*vars = append(*vars, EnvVar{Chain: newChain})
				}
			} else {
				// Fallback to primitive if expansion failed
				*vars = append(*vars, EnvVar{Chain: chain})
			}
		} else {
			// Treat as primitive (interfaces, unknown external types, etc.)
			*vars = append(*vars, EnvVar{Chain: chain})
		}
	case *ast.StructType:
		for _, f := range t.Fields.List {
			processFieldWithResolver(f, chain, vars, resolver)
		}
	}
}

// processFieldWithResolver handles a single struct field with type resolution
func processFieldWithResolver(field *ast.Field, parentChain []Field, vars *[]EnvVar, resolver *TypeResolver) {
	if field.Names == nil {
		processStructFieldsWithResolver(field.Type, parentChain, vars, resolver)
		return
	}

	var yamlTag string
	if field.Tag != nil {
		yamlTag = extractYAMLFieldName(field.Tag.Value, field.Names[0].Name)
	}
	fieldName := yamlTag
	if fieldName == "" {
		fieldName = strings.ToLower(field.Names[0].Name)
	}

	// Get field comment
	var comment string
	if field.Doc != nil {
		comments := make([]string, 0, len(field.Doc.List))
		for _, c := range field.Doc.List {
			comments = append(comments, strings.TrimSpace(strings.TrimPrefix(c.Text, "//")))
		}
		comment = strings.Join(comments, " ")
	}

	newField := Field{
		Name:    fieldName,
		Type:    getTypeString(field.Type),
		Comment: comment,
	}
	chain := make([]Field, len(parentChain))
	copy(chain, parentChain)
	chain = append(chain, newField)

	processStructFieldsWithResolver(field.Type, chain, vars, resolver)
}

// getEnvExampleValue returns an example value for environment variables based on the type
func getEnvExampleValue(fieldType string) string {
	baseType := strings.TrimPrefix(fieldType, "*")
	switch {
	case baseType == "string":
		return "string"
	case strings.HasPrefix(baseType, "int") || strings.HasPrefix(baseType, "uint"):
		return "integer"
	case strings.HasPrefix(baseType, "float"):
		return "number"
	case baseType == "bool":
		return "true/false"
	default:
		return "string"
	}
}

func printEnvText(prefix string, vars []EnvVar) {
	fmt.Println("Environment variable paths:")
	fmt.Println("NAME                           VALUE           DESCRIPTION")
	fmt.Println("----                          -----           -----------")
	for _, v := range vars {
		lastField := v.LastField()
		if lastField.Comment != "" {
			fmt.Printf("%-30s %-15s // %s\n", v.Path(prefix), getEnvExampleValue(lastField.Type), lastField.Comment)
		} else {
			fmt.Printf("%-30s %s\n", v.Path(prefix), getEnvExampleValue(lastField.Type))
		}
	}
}

func printEnvMarkdown(prefix string, vars []EnvVar) {
	fmt.Println("| Environment Variable | Expected Value | Description |")
	fmt.Println("|---------------------|----------------|-------------|")
	for _, v := range vars {
		lastField := v.LastField()
		comment := lastField.Comment
		if comment == "" {
			comment = "-"
		}
		fmt.Printf("| `%s` | `%s` | %s |\n", v.Path(prefix), getEnvExampleValue(lastField.Type), comment)
	}
}

func printYAMLSample(prefix string, vars []EnvVar) {
	printed := make(map[string]bool)
	for _, v := range vars {
		path := v.YAMLPath()
		parts := strings.Split(path, ".")

		// Print each level of nesting
		current := ""
		indent := ""
		for i, part := range parts {
			if i == len(parts)-1 {
				// Last part - print with a sample value based on type
				fmt.Printf("%s%s: %s\n", indent, part, getEnvExampleValue(v.LastField().Type))
			} else {
				if current != "" {
					current += "."
				}
				current += part
				if !printed[current] {
					fmt.Printf("%s%s:\n", indent, part)
					printed[current] = true
				}
				indent += "  "
			}
		}
	}
}

package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
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

	var configStruct *ast.StructType
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		filePath := filepath.Join(path, entry.Name())
		fs := token.NewFileSet()
		node, err := parser.ParseFile(fs, filePath, nil, parser.ParseComments)
		if err != nil {
			continue
		}

		ast.Inspect(node, func(n ast.Node) bool {
			if ts, ok := n.(*ast.TypeSpec); ok {
				if st, ok := ts.Type.(*ast.StructType); ok {
					if ts.Name.Name == configStructName {
						configStruct = st
						return false
					}
				}
			}
			return true
		})
		if configStruct != nil {
			break
		}
	}

	if configStruct == nil {
		return errors.New("Config struct not found")
	}

	vars := make([]EnvVar, 0)
	for _, field := range configStruct.Fields.List {
		processField(field, nil, &vars)
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

// isPrimitiveType returns true if the type is a primitive or string type
func isPrimitiveType(typeStr string) bool {
	primitives := map[string]bool{
		"string": true,
		"int":    true,
		"bool":   true,
	}
	return primitives[strings.TrimPrefix(typeStr, "*")]
}

// getYAMLTag extracts the yaml tag value from a field tag
func getYAMLTag(tag string) string {
	if tag == "" {
		return ""
	}
	tag = strings.Trim(tag, "`")
	for _, tagPart := range strings.Split(tag, " ") {
		if strings.HasPrefix(tagPart, "yaml:") {
			// Extract the yaml tag content
			content := strings.Trim(strings.Split(tagPart, ":")[1], "\"")
			// Split by comma and take the first part as the field name
			return strings.Split(content, ",")[0]
		}
	}
	return ""
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

// processStructFields recursively processes struct fields and builds environment variable paths
func processStructFields(field ast.Expr, chain []Field, vars *[]EnvVar) {
	switch t := field.(type) {
	case *ast.Ident:
		if isPrimitiveType(t.Name) {
			*vars = append(*vars, EnvVar{Chain: chain})
		} else if obj := t.Obj; obj != nil {
			if ts, ok := obj.Decl.(*ast.TypeSpec); ok {
				if st, ok := ts.Type.(*ast.StructType); ok {
					for _, f := range st.Fields.List {
						processField(f, chain, vars)
					}
				}
			}
		}
	case *ast.StarExpr:
		if isPrimitiveType(getTypeString(t.X)) {
			*vars = append(*vars, EnvVar{Chain: chain})
		} else {
			processStructFields(t.X, chain, vars)
		}
	case *ast.SelectorExpr:
		*vars = append(*vars, EnvVar{Chain: chain})
	case *ast.StructType:
		for _, f := range t.Fields.List {
			processField(f, chain, vars)
		}
	}
}

// processField handles a single struct field
func processField(field *ast.Field, parentChain []Field, vars *[]EnvVar) {
	if field.Names == nil {
		processStructFields(field.Type, parentChain, vars)
		return
	}

	yamlTag := getYAMLTag(field.Tag.Value)
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

	processStructFields(field.Type, chain, vars)
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

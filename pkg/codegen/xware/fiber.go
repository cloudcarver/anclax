package codegen

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/oapi-codegen/oapi-codegen/v2/pkg/codegen"
	"github.com/pkg/errors"
)

//go:embed templates
var src embed.FS

func Generate(workdir, packageName string, specPath string, outPath string) error {
	doc, err := getSchema(filepath.Join(workdir, specPath))
	if err != nil {
		return errors.Wrap(err, "failed to get schema")
	}

	codegen.SetGlobalStateSpec(doc)

	code, err := generateMiddlewares(doc)
	if err != nil {
		return errors.Wrap(err, "failed to generate middlewares")
	}

	checkRules, err := generateCheckRules(doc)
	if err != nil {
		return errors.Wrap(err, "failed to generate check rules")
	}

	// Check if generated code contains openapi_types references
	imports := `import "github.com/gofiber/fiber/v2"`
	if strings.Contains(code, "openapi_types.") || strings.Contains(checkRules, "openapi_types.") {
		imports = `import (
	"github.com/gofiber/fiber/v2"
	openapi_types "github.com/oapi-codegen/runtime/types"
)`
	}

	code = `package ` + packageName + `

` + imports + `

` + checkRules + `

` + code

	if err := os.WriteFile(filepath.Join(workdir, outPath), []byte(code), 0644); err != nil {
		return errors.Wrap(err, "failed to write file")
	}

	return nil
}

func getSchema(specPath string) (*openapi3.T, error) {
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile(specPath)
	if err != nil {
		return nil, err
	}
	if err := loader.ResolveRefsIn(doc, &url.URL{}); err != nil {
		return nil, err
	}
	return doc, nil
}

type TmplVar struct {
	PackageName string
}

func generateMiddlewares(doc *openapi3.T) (string, error) {
	// Include our XTmplFuncs in the template functions
	funcMap := template.FuncMap{}
	for k, v := range codegen.TemplateFunctions {
		funcMap[k] = v
	}
	for k, v := range XTmplFuncs {
		funcMap[k] = v
	}

	t := template.New("oapi-codegen-fiber").Funcs(funcMap)

	if err := codegen.LoadTemplates(src, t); err != nil {
		return "", errors.Wrap(err, "failed to load templates")
	}

	ops, err := codegen.OperationDefinitions(doc, true)
	if err != nil {
		return "", errors.Wrap(err, "failed to get operation definitions")
	}

	opsWithSecurity := make([]codegen.OperationDefinition, 0, len(ops))
	for _, op := range ops {
		if len(op.SecurityDefinitions) > 0 {
			opsWithSecurity = append(opsWithSecurity, op)
		}
	}

	code, err := codegen.GenerateTemplates([]string{"middlewares.tmpl"}, t, opsWithSecurity)
	if err != nil {
		return "", errors.Wrap(err, "failed to generate templates")
	}
	return code, nil
}

var XTmplFuncs = template.FuncMap{
	"genXParamArgs": func(params []XParam) string {
		ret := ""
		for i, param := range params {
			ret += fmt.Sprintf("%s %s", param.Name, param.Schema.GoType)
			if i < len(params)-1 {
				ret += ", "
			}
		}
		return ret
	},
	"contains": strings.Contains,
}

func generateCheckRules(doc *openapi3.T) (string, error) {
	checkRules, err := parseXCheckRules(doc)
	if err != nil {
		return "", errors.Wrap(err, "failed to parse check rules")
	}

	functions, err := parseXFunctions(doc)
	if err != nil {
		return "", errors.Wrap(err, "failed to parse functions")
	}

	templateContent, err := src.ReadFile("templates/funcs.tmpl")
	if err != nil {
		return "", errors.Wrap(err, "failed to read template file")
	}

	// Create and parse the template with our custom functions
	t := template.Must(template.New("funcs").Funcs(XTmplFuncs).Parse(string(templateContent)))

	var buf bytes.Buffer
	if err := t.Execute(&buf, map[string]any{
		"CheckRules": checkRules,
		"Functions":  functions,
	}); err != nil {
		return "", errors.Wrap(err, "failed to execute template")
	}

	return buf.String(), nil
}

type XFunction struct {
	Name        string
	UseContext  bool
	Description string
	Params      []XParam
	Return      XParam
}

type XCheckRule struct {
	Name        string
	UseContext  bool
	Description string
	Params      []XParam
}

type XParam struct {
	Name        string
	Description string
	Schema      codegen.Schema
}

type RawCheckRule struct {
	UseContext  bool       `json:"useContext"`
	Description string     `json:"description"`
	Parameters  []RawParam `json:"parameters"`
}

type RawFunction struct {
	UseContext  bool       `json:"useContext"`
	Description string     `json:"description"`
	Params      []RawParam `json:"params"`
	Return      RawParam   `json:"return"`
}

type RawParam struct {
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Schema      *openapi3.SchemaRef `json:"schema"`
}

func jsonParse[T any](raw map[string]any, out *T) error {
	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(raw)
	return json.Unmarshal(buf.Bytes(), out)
}

func parseXCheckRules(doc *openapi3.T) ([]XCheckRule, error) {
	if _, exist := doc.Extensions["x-check-rules"]; !exist {
		return nil, nil
	}

	var ret []XCheckRule

	var raw map[string]RawCheckRule
	rawCheckRules, ok := doc.Extensions["x-check-rules"].(map[string]any)
	if !ok {
		return nil, errors.New("x-check-rules is not a map")
	}
	if err := jsonParse(rawCheckRules, &raw); err != nil {
		return nil, errors.Wrap(err, "failed to parse check rules")
	}
	for name, rule := range raw {
		item := XCheckRule{
			Name:        name,
			Description: rule.Description,
			UseContext:  rule.UseContext,
		}
		for _, param := range rule.Parameters {
			goschema, err := codegen.GenerateGoSchema(param.Schema, []string{})
			if err != nil {
				return nil, errors.Wrap(err, "failed to generate go schema")
			}
			item.Params = append(item.Params, XParam{
				Name:        param.Name,
				Description: param.Description,
				Schema:      goschema,
			})
		}
		ret = append(ret, item)
	}
	sort.Slice(ret, func(i, j int) bool {
		return ret[i].Name < ret[j].Name
	})
	return ret, nil
}

func parseXFunctions(doc *openapi3.T) ([]XFunction, error) {
	if _, exist := doc.Extensions["x-functions"]; !exist {
		return nil, nil
	}

	var ret []XFunction

	var raw map[string]RawFunction
	rawFunctions, ok := doc.Extensions["x-functions"].(map[string]any)
	if !ok {
		return nil, errors.New("x-functions is not a map")
	}
	if err := jsonParse(rawFunctions, &raw); err != nil {
		return nil, errors.Wrap(err, "failed to parse functions")
	}
	for name, function := range raw {
		item := XFunction{
			Name:        name,
			Description: function.Description,
			UseContext:  function.UseContext,
		}
		for _, param := range function.Params {
			goschema, err := codegen.GenerateGoSchema(param.Schema, []string{})
			if err != nil {
				return nil, errors.Wrap(err, "failed to generate go schema")
			}
			item.Params = append(item.Params, XParam{
				Name:        param.Name,
				Description: param.Description,
				Schema:      goschema,
			})
		}
		goschema, err := codegen.GenerateGoSchema(function.Return.Schema, []string{})
		if err != nil {
			return nil, errors.Wrap(err, "failed to generate go schema")
		}
		item.Return = XParam{
			Name:        function.Return.Name,
			Description: function.Return.Description,
			Schema:      goschema,
		}
		ret = append(ret, item)
	}
	sort.Slice(ret, func(i, j int) bool {
		return ret[i].Name < ret[j].Name
	})
	return ret, nil
}

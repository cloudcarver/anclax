package codegen

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

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/oapi-codegen/oapi-codegen/v2/pkg/util"
	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Path          string
	Out           string
	MiddlewareOut string
	Package       string
	RawConfig     any
}

type rawConfig struct {
	OutputOptions struct {
		InitialismOverrides bool `yaml:"initialism-overrides,omitempty"`
		Overlay             struct {
			Path   string `yaml:"path,omitempty"`
			Strict *bool  `yaml:"strict,omitempty"`
		} `yaml:"overlay,omitempty"`
	} `yaml:"output-options,omitempty"`
}

type generatorOptions struct {
	InitialismOverrides bool
	OverlayPath         string
	OverlayStrict       bool
}

type document struct {
	PackageName      string
	SecurityConsts   []securityConst
	Schemas          []schemaDef
	Enums            []enumDef
	Operations       []operationDef
	CheckRules       []xCheckRule
	Functions        []xFunction
	SpecTypeImports  map[string]struct{}
	ScopeTypeImports map[string]struct{}
}

type securityConst struct {
	Name  string
	Value string
}

type schemaDef struct {
	Name        string
	Description string
	Kind        string
	AliasType   string
	Fields      []fieldDef
}

type fieldDef struct {
	Name        string
	JSONName    string
	Type        string
	Description string
	Optional    bool
}

type enumDef struct {
	Name        string
	Type        string
	Description string
	Values      []enumValue
}

type enumValue struct {
	Name    string
	Literal string
}

type operationDef struct {
	Name          string
	Summary       string
	Method        string
	Path          string
	FiberPath     string
	PathFormat    string
	PathArgs      string
	PathParams    []paramDef
	RequestBody   *requestBodyDef
	Responses     []responseDef
	Securities    []operationSecurity
	NeedsAuth     bool
	NeedsBody     bool
	NeedsResponse bool
}

type requestBodyDef struct {
	AliasName    string
	GoType       string
	ContentType  string
	WrapperName  string
	HasJSONAlias bool
}

type responseDef struct {
	StatusCode int
	GoType     string
	FieldName  string
	HasJSON    bool
}

type operationSecurity struct {
	ConstName string
	Scopes    []string
}

type paramDef struct {
	Name       string
	VarName    string
	GoName     string
	Type       string
	SourceName string
}

type resolvedType struct {
	GoType  string
	Imports []string
}

type rawCheckRule struct {
	UseContext  bool       `json:"useContext"`
	Description string     `json:"description"`
	Parameters  []rawParam `json:"parameters"`
}

type rawFunction struct {
	UseContext  bool       `json:"useContext"`
	Description string     `json:"description"`
	Params      []rawParam `json:"params"`
	Return      rawParam   `json:"return"`
}

type rawParam struct {
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Schema      *openapi3.SchemaRef `json:"schema"`
}

type xCheckRule struct {
	Name        string
	UseContext  bool
	Description string
	Params      []xParam
}

type xFunction struct {
	Name        string
	UseContext  bool
	Description string
	Params      []xParam
	Return      xParam
}

type xParam struct {
	Name        string
	Description string
	Type        string
}

func Generate(workdir string, config Config) error {
	if config.Path == "" {
		return errors.New("oapi-codegen path is required")
	}
	if config.Out == "" {
		return errors.New("oapi-codegen out is required")
	}
	if config.Package == "" {
		return errors.New("oapi-codegen package is required")
	}
	if config.MiddlewareOut == "" {
		config.MiddlewareOut = defaultMiddlewareOut(config.Out)
	}

	opts, err := parseConfig(config.RawConfig)
	if err != nil {
		return errors.Wrap(err, "failed to parse oapi config")
	}

	specPath := config.Path
	if !filepath.IsAbs(specPath) {
		specPath = filepath.Join(workdir, specPath)
	}
	swagger, err := loadSwagger(workdir, specPath, opts)
	if err != nil {
		return errors.Wrap(err, "failed to load OpenAPI spec")
	}

	doc, err := buildDocument(swagger, config.Package)
	if err != nil {
		return errors.Wrap(err, "failed to build OpenAPI document")
	}

	specCode, err := renderSpec(doc)
	if err != nil {
		return errors.Wrap(err, "failed to render OpenAPI code")
	}
	if err := writeFile(workdir, config.Out, specCode); err != nil {
		return errors.Wrap(err, "failed to write OpenAPI output")
	}

	scopeCode, err := renderMiddleware(doc)
	if err != nil {
		return errors.Wrap(err, "failed to render middleware extensions")
	}
	if err := writeFile(workdir, config.MiddlewareOut, scopeCode); err != nil {
		return errors.Wrap(err, "failed to write middleware extensions")
	}

	return nil
}

func parseConfig(raw any) (generatorOptions, error) {
	opts := generatorOptions{OverlayStrict: true}
	if raw == nil {
		return opts, nil
	}

	var cfg rawConfig
	buf, err := yaml.Marshal(raw)
	if err != nil {
		return opts, err
	}
	if err := yaml.Unmarshal(buf, &cfg); err != nil {
		return opts, err
	}
	opts.InitialismOverrides = cfg.OutputOptions.InitialismOverrides
	opts.OverlayPath = cfg.OutputOptions.Overlay.Path
	if cfg.OutputOptions.Overlay.Strict != nil {
		opts.OverlayStrict = *cfg.OutputOptions.Overlay.Strict
	}
	return opts, nil
}

func loadSwagger(workdir, specPath string, opts generatorOptions) (*openapi3.T, error) {
	overlayPath := opts.OverlayPath
	if overlayPath != "" && !filepath.IsAbs(overlayPath) {
		overlayPath = filepath.Join(workdir, overlayPath)
	}
	return util.LoadSwaggerWithOverlay(specPath, util.LoadSwaggerWithOverlayOpts{
		Path:   overlayPath,
		Strict: opts.OverlayStrict,
	})
}

func buildDocument(spec *openapi3.T, packageName string) (*document, error) {
	doc := &document{
		PackageName:      packageName,
		SpecTypeImports:  map[string]struct{}{},
		ScopeTypeImports: map[string]struct{}{},
	}
	enumMap := map[string]*enumDef{}
	securityMap := map[string]securityConst{}

	if spec.Components.Schemas != nil {
		schemaNames := make([]string, 0, len(spec.Components.Schemas))
		for name := range spec.Components.Schemas {
			schemaNames = append(schemaNames, name)
		}
		sort.Strings(schemaNames)
		for _, name := range schemaNames {
			schema, err := buildSchemaDef(name, spec.Components.Schemas[name], enumMap, doc.SpecTypeImports)
			if err != nil {
				return nil, errors.Wrapf(err, "failed to build schema %s", name)
			}
			doc.Schemas = append(doc.Schemas, schema)
		}
	}

	if spec.Paths != nil {
		for _, path := range spec.Paths.InMatchingOrder() {
			pathItem := spec.Paths.Value(path)
			if pathItem == nil {
				continue
			}
			for _, opItem := range orderedOperations(pathItem) {
				op, err := buildOperation(path, pathItem, opItem.method, opItem.operation, enumMap, doc.SpecTypeImports)
				if err != nil {
					return nil, errors.Wrapf(err, "failed to build operation %s %s", opItem.method, path)
				}
				doc.Operations = append(doc.Operations, op)
				for _, sec := range op.Securities {
					securityMap[sec.ConstName] = securityConst{Name: sec.ConstName, Value: strings.TrimSuffix(sec.ConstName, "Scopes") + ".Scopes"}
				}
			}
		}
	}

	if err := parseXCheckRules(doc, spec, enumMap); err != nil {
		return nil, errors.Wrap(err, "failed to parse x-check-rules")
	}
	if err := parseXFunctions(doc, spec, enumMap); err != nil {
		return nil, errors.Wrap(err, "failed to parse x-functions")
	}

	for _, enum := range enumMap {
		doc.Enums = append(doc.Enums, *enum)
	}
	sort.Slice(doc.Enums, func(i, j int) bool { return doc.Enums[i].Name < doc.Enums[j].Name })

	for _, sec := range securityMap {
		doc.SecurityConsts = append(doc.SecurityConsts, sec)
	}
	sort.Slice(doc.SecurityConsts, func(i, j int) bool { return doc.SecurityConsts[i].Name < doc.SecurityConsts[j].Name })

	sort.Slice(doc.CheckRules, func(i, j int) bool { return doc.CheckRules[i].Name < doc.CheckRules[j].Name })
	sort.Slice(doc.Functions, func(i, j int) bool { return doc.Functions[i].Name < doc.Functions[j].Name })

	return doc, nil
}

func buildSchemaDef(name string, ref *openapi3.SchemaRef, enumMap map[string]*enumDef, imports map[string]struct{}) (schemaDef, error) {
	schema := schemaDef{Name: exportName(name), Description: exportName(name)}
	if ref == nil || ref.Value == nil {
		return schema, nil
	}
	value := ref.Value
	schema.Description = firstNonEmpty(value.Description, schema.Name)

	resolved, err := resolveType(ref, schema.Name, enumMap)
	if err != nil {
		return schema, err
	}
	mergeImports(imports, resolved.Imports...)

	if value.Type != nil && value.Type.Is("object") || len(value.Properties) > 0 {
		schema.Kind = "object"
		required := requiredSet(value.Required)
		propNames := make([]string, 0, len(value.Properties))
		for prop := range value.Properties {
			propNames = append(propNames, prop)
		}
		sort.Strings(propNames)
		for _, propName := range propNames {
			propRef := value.Properties[propName]
			fieldResolved, err := resolveType(propRef, schema.Name+exportName(propName), enumMap)
			if err != nil {
				return schema, err
			}
			mergeImports(imports, fieldResolved.Imports...)
			fieldType := fieldResolved.GoType
			optional := !required[propName]
			if optional {
				fieldType = pointerType(fieldType)
			}
			field := fieldDef{
				Name:        exportName(propName),
				JSONName:    propName,
				Type:        fieldType,
				Description: firstNonEmpty(propRef.Value.Description, ""),
				Optional:    optional,
			}
			schema.Fields = append(schema.Fields, field)
		}
		return schema, nil
	}

	schema.Kind = "alias"
	schema.AliasType = resolved.GoType
	return schema, nil
}

func buildOperation(path string, pathItem *openapi3.PathItem, method string, op *openapi3.Operation, enumMap map[string]*enumDef, imports map[string]struct{}) (operationDef, error) {
	name := exportName(op.OperationID)
	if name == "" {
		name = exportName(strings.Trim(path, "/")) + exportName(strings.ToLower(method))
	}
	ret := operationDef{
		Name:      name,
		Summary:   firstNonEmpty(op.Summary, name),
		Method:    strings.ToUpper(method),
		Path:      path,
		FiberPath: fiberPath(path),
	}

	params, err := collectPathParams(path, pathItem, op)
	if err != nil {
		return ret, err
	}
	ret.PathParams = params
	ret.PathFormat, ret.PathArgs = buildPathFormatter(path, params)

	if op.RequestBody != nil && op.RequestBody.Value != nil {
		if mediaType, schemaRef, ok := jsonBodySchema(op.RequestBody.Value.Content); ok {
			resolved, err := resolveType(schemaRef, name+"RequestBody", enumMap)
			if err != nil {
				return ret, err
			}
			mergeImports(imports, resolved.Imports...)
			ret.RequestBody = &requestBodyDef{
				AliasName:    name + "JSONRequestBody",
				GoType:       resolved.GoType,
				ContentType:  mediaType,
				WrapperName:  name,
				HasJSONAlias: true,
			}
			ret.NeedsBody = true
		}
	}

	if op.Responses != nil {
		responseKeys := make([]string, 0, len(op.Responses.Map()))
		for code := range op.Responses.Map() {
			responseKeys = append(responseKeys, code)
		}
		sort.Slice(responseKeys, func(i, j int) bool { return responseSortKey(responseKeys[i]) < responseSortKey(responseKeys[j]) })
		for _, code := range responseKeys {
			statusCode, err := strconv.Atoi(code)
			if err != nil {
				continue
			}
			responseRef := op.Responses.Map()[code]
			if responseRef == nil || responseRef.Value == nil {
				ret.Responses = append(ret.Responses, responseDef{StatusCode: statusCode})
				continue
			}
			response := responseDef{StatusCode: statusCode}
			if _, schemaRef, ok := jsonResponseSchema(responseRef.Value.Content); ok {
				resolved, err := resolveType(schemaRef, name+"Response"+code, enumMap)
				if err != nil {
					return ret, err
				}
				mergeImports(imports, resolved.Imports...)
				response.GoType = resolved.GoType
				response.FieldName = "JSON" + code
				response.HasJSON = true
			}
			ret.Responses = append(ret.Responses, response)
		}
	}
	ret.NeedsResponse = true

	if op.Security != nil {
		for _, requirement := range *op.Security {
			for scheme, scopes := range requirement {
				ret.Securities = append(ret.Securities, operationSecurity{
					ConstName: exportName(scheme) + "Scopes",
					Scopes:    append([]string(nil), scopes...),
				})
			}
		}
	}
	ret.NeedsAuth = len(ret.Securities) > 0

	return ret, nil
}

func renderSpec(doc *document) (string, error) {
	imports := specImports(doc)
	var b strings.Builder
	b.WriteString("// Package ")
	b.WriteString(doc.PackageName)
	b.WriteString(" provides primitives to interact with the openapi HTTP API.\n//\n")
	b.WriteString("// Code generated by github.com/cloudcarver/anclax DO NOT EDIT.\n")
	b.WriteString("package ")
	b.WriteString(doc.PackageName)
	b.WriteString("\n\n")
	renderImports(&b, imports)

	if len(doc.SecurityConsts) > 0 {
		b.WriteString("const (\n")
		for _, sec := range doc.SecurityConsts {
			b.WriteString("\t")
			b.WriteString(sec.Name)
			b.WriteString(" = ")
			b.WriteString(strconv.Quote(sec.Value))
			b.WriteString("\n")
		}
		b.WriteString(")\n\n")
	}

	for _, enum := range doc.Enums {
		b.WriteString("// Defines values for ")
		b.WriteString(enum.Name)
		b.WriteString(".\n")
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

	for _, schema := range doc.Schemas {
		renderSchema(&b, schema)
	}

	for _, enum := range doc.Enums {
		b.WriteString("// ")
		b.WriteString(enum.Name)
		if enum.Description != "" {
			b.WriteString(" ")
			b.WriteString(enum.Description)
		} else {
			b.WriteString(" defines enum values")
		}
		b.WriteString("\n")
		b.WriteString("type ")
		b.WriteString(enum.Name)
		b.WriteString(" ")
		b.WriteString(enum.Type)
		b.WriteString("\n\n")
	}

	for _, op := range doc.Operations {
		if op.RequestBody != nil && op.RequestBody.HasJSONAlias {
			b.WriteString("// ")
			b.WriteString(op.RequestBody.AliasName)
			b.WriteString(" defines body for ")
			b.WriteString(op.Name)
			b.WriteString(" for application/json ContentType.\n")
			b.WriteString("type ")
			b.WriteString(op.RequestBody.AliasName)
			b.WriteString(" = ")
			b.WriteString(op.RequestBody.GoType)
			b.WriteString("\n\n")
		}
	}

	renderClient(&b, doc)
	renderServer(&b, doc)

	formatted, err := format.Source([]byte(b.String()))
	if err != nil {
		return "", errors.Wrap(err, "failed to format generated OpenAPI code")
	}
	return string(formatted), nil
}

func renderMiddleware(doc *document) (string, error) {
	imports := scopeImports(doc)
	var b strings.Builder
	b.WriteString("package ")
	b.WriteString(doc.PackageName)
	b.WriteString("\n\n")
	renderImports(&b, imports)

	b.WriteString("type Validator interface {\n")
	b.WriteString("\t// AuthFunc is called before the request is processed. The response will be 401 if the auth fails.\n")
	b.WriteString("\tAuthFunc(*fiber.Ctx) error\n\n")
	b.WriteString("\t// PreValidate is called before the request is processed. The response will be 403 if the validation fails.\n")
	b.WriteString("\tPreValidate(*fiber.Ctx) error\n\n")
	b.WriteString("\t// PostValidate is called after the request is processed. The response will be 403 if the validation fails.\n")
	b.WriteString("\tPostValidate(*fiber.Ctx) error\n")
	if len(doc.CheckRules) > 0 || len(doc.Functions) > 0 {
		b.WriteString("\n")
	}
	for _, rule := range doc.CheckRules {
		b.WriteString("\t")
		b.WriteString(rule.Name)
		b.WriteString("(")
		b.WriteString(scopeReceiver(rule.UseContext))
		if len(rule.Params) > 0 {
			b.WriteString(", ")
			for i, param := range rule.Params {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(param.Name)
				b.WriteString(" ")
				b.WriteString(param.Type)
			}
		}
		b.WriteString(") error\n")
	}
	if len(doc.CheckRules) > 0 && len(doc.Functions) > 0 {
		b.WriteString("\n")
	}
	for _, fn := range doc.Functions {
		b.WriteString("\t")
		b.WriteString(fn.Name)
		b.WriteString("(")
		b.WriteString(scopeReceiver(fn.UseContext))
		if len(fn.Params) > 0 {
			b.WriteString(", ")
			for i, param := range fn.Params {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(param.Name)
				b.WriteString(" ")
				b.WriteString(param.Type)
			}
		}
		b.WriteString(") ")
		b.WriteString(fn.Return.Type)
		b.WriteString("\n")
	}
	b.WriteString("}\n\n")

	b.WriteString("type XMiddleware struct {\n\tServerInterface\n\tValidator\n}\n\n")
	b.WriteString("func NewXMiddleware(handler ServerInterface, validator Validator) ServerInterface {\n")
	b.WriteString("\treturn &XMiddleware{ServerInterface: handler, Validator: validator}\n")
	b.WriteString("}\n\n")

	for _, op := range doc.Operations {
		if !op.NeedsAuth {
			continue
		}
		b.WriteString("// ")
		b.WriteString(op.Summary)
		b.WriteString("\n")
		b.WriteString("// (")
		b.WriteString(op.Method)
		b.WriteString(" ")
		b.WriteString(op.Path)
		b.WriteString(")\n")
		b.WriteString("func (x *XMiddleware) ")
		b.WriteString(op.Name)
		b.WriteString("(c *fiber.Ctx")
		for _, param := range op.PathParams {
			b.WriteString(", ")
			b.WriteString(param.VarName)
			b.WriteString(" ")
			b.WriteString(param.Type)
		}
		b.WriteString(") error {\n")
		b.WriteString("\tif err := x.AuthFunc(c); err != nil {\n")
		b.WriteString("\t\treturn c.Status(fiber.StatusUnauthorized).SendString(err.Error())\n")
		b.WriteString("\t}\n")
		b.WriteString("\tif err := x.PreValidate(c); err != nil {\n")
		b.WriteString("\t\treturn c.Status(fiber.StatusForbidden).SendString(err.Error())\n")
		b.WriteString("\t}\n")
		if operationNeedsOperationID(op) {
			b.WriteString("\toperationID := ")
			b.WriteString(strconv.Quote(op.Name))
			b.WriteString("\n")
		}
		for _, sec := range op.Securities {
			for _, scope := range sec.Scopes {
				b.WriteString("\tif err := ")
				b.WriteString(scope)
				b.WriteString("; err != nil {\n")
				b.WriteString("\t\treturn c.Status(fiber.StatusForbidden).SendString(err.Error())\n")
				b.WriteString("\t}\n")
			}
		}
		b.WriteString("\tif err := x.PostValidate(c); err != nil {\n")
		b.WriteString("\t\treturn c.Status(fiber.StatusForbidden).SendString(err.Error())\n")
		b.WriteString("\t}\n")
		b.WriteString("\treturn x.ServerInterface.")
		b.WriteString(op.Name)
		b.WriteString("(c")
		for _, param := range op.PathParams {
			b.WriteString(", ")
			b.WriteString(param.VarName)
		}
		b.WriteString(")\n")
		b.WriteString("}\n\n")
	}

	formatted, err := format.Source([]byte(b.String()))
	if err != nil {
		return "", errors.Wrap(err, "failed to format generated middleware code")
	}
	return string(formatted), nil
}

func renderSchema(b *strings.Builder, schema schemaDef) {
	if schema.Kind == "alias" {
		b.WriteString("// ")
		b.WriteString(schema.Name)
		b.WriteString(" defines model for ")
		b.WriteString(schema.Name)
		b.WriteString(".\n")
		b.WriteString("type ")
		b.WriteString(schema.Name)
		b.WriteString(" ")
		b.WriteString(schema.AliasType)
		b.WriteString("\n\n")
		return
	}

	b.WriteString("// ")
	b.WriteString(schema.Name)
	b.WriteString(" defines model for ")
	b.WriteString(schema.Name)
	b.WriteString(".\n")
	b.WriteString("type ")
	b.WriteString(schema.Name)
	b.WriteString(" struct {\n")
	for _, field := range schema.Fields {
		writeComment(b, field.Description, "\t")
		b.WriteString("\t")
		b.WriteString(field.Name)
		b.WriteString(" ")
		b.WriteString(field.Type)
		b.WriteString(" `json:")
		b.WriteString(strconv.Quote(jsonTag(field.JSONName, field.Optional)))
		b.WriteString("`\n")
	}
	b.WriteString("}\n\n")
}

func renderClient(b *strings.Builder, doc *document) {
	b.WriteString("// RequestEditorFn is the function signature for the RequestEditor callback function\n")
	b.WriteString("type RequestEditorFn func(ctx context.Context, req *http.Request) error\n\n")
	b.WriteString("// Doer performs HTTP requests.\n")
	b.WriteString("//\n")
	b.WriteString("// The standard http.Client implements this interface.\n")
	b.WriteString("type HttpRequestDoer interface {\n\tDo(req *http.Request) (*http.Response, error)\n}\n\n")
	b.WriteString("// Client which conforms to the OpenAPI3 specification for this service.\n")
	b.WriteString("type Client struct {\n")
	b.WriteString("\t// The endpoint of the server conforming to this interface, with scheme,\n")
	b.WriteString("\t// https://api.deepmap.com for example. This can contain a path relative\n")
	b.WriteString("\t// to the server, such as https://api.deepmap.com/dev-test, and all the\n")
	b.WriteString("\t// paths in the swagger spec will be appended to the server.\n")
	b.WriteString("\tServer string\n\n")
	b.WriteString("\t// Doer for performing requests, typically a *http.Client with any\n")
	b.WriteString("\t// customized settings, such as certificate chains.\n")
	b.WriteString("\tClient HttpRequestDoer\n\n")
	b.WriteString("\t// A list of callbacks for modifying requests which are generated before sending over\n")
	b.WriteString("\t// the network.\n")
	b.WriteString("\tRequestEditors []RequestEditorFn\n")
	b.WriteString("}\n\n")
	b.WriteString("// ClientOption allows setting custom parameters during construction\n")
	b.WriteString("type ClientOption func(*Client) error\n\n")
	b.WriteString("// Creates a new Client, with reasonable defaults\n")
	b.WriteString("func NewClient(server string, opts ...ClientOption) (*Client, error) {\n")
	b.WriteString("\tclient := Client{Server: server}\n")
	b.WriteString("\tfor _, o := range opts {\n")
	b.WriteString("\t\tif err := o(&client); err != nil {\n\t\t\treturn nil, err\n\t\t}\n\t}\n")
	b.WriteString("\tif !strings.HasSuffix(client.Server, \"/\") {\n\t\tclient.Server += \"/\"\n\t}\n")
	b.WriteString("\tif client.Client == nil {\n\t\tclient.Client = &http.Client{}\n\t}\n")
	b.WriteString("\treturn &client, nil\n")
	b.WriteString("}\n\n")
	b.WriteString("// WithHTTPClient allows overriding the default Doer, which is\n")
	b.WriteString("// automatically created using http.Client. This is useful for tests.\n")
	b.WriteString("func WithHTTPClient(doer HttpRequestDoer) ClientOption {\n")
	b.WriteString("\treturn func(c *Client) error {\n\t\tc.Client = doer\n\t\treturn nil\n\t}\n")
	b.WriteString("}\n\n")
	b.WriteString("// WithRequestEditorFn allows setting up a callback function, which will be\n")
	b.WriteString("// called right before sending the request. This can be used to mutate the request.\n")
	b.WriteString("func WithRequestEditorFn(fn RequestEditorFn) ClientOption {\n")
	b.WriteString("\treturn func(c *Client) error {\n\t\tc.RequestEditors = append(c.RequestEditors, fn)\n\t\treturn nil\n\t}\n")
	b.WriteString("}\n\n")
	b.WriteString("// The interface specification for the client above.\n")
	b.WriteString("type ClientInterface interface {\n")
	for _, op := range doc.Operations {
		if op.RequestBody != nil {
			b.WriteString("\t// ")
			b.WriteString(op.Name)
			b.WriteString("WithBody request with any body\n")
			b.WriteString("\t")
			b.WriteString(op.Name)
			b.WriteString("WithBody(ctx context.Context")
			for _, param := range op.PathParams {
				b.WriteString(", ")
				b.WriteString(param.VarName)
				b.WriteString(" ")
				b.WriteString(param.Type)
			}
			b.WriteString(", contentType string, body io.Reader, reqEditors ...RequestEditorFn) (*http.Response, error)\n\n")
			b.WriteString("\t")
			b.WriteString(op.Name)
			b.WriteString("(ctx context.Context")
			for _, param := range op.PathParams {
				b.WriteString(", ")
				b.WriteString(param.VarName)
				b.WriteString(" ")
				b.WriteString(param.Type)
			}
			b.WriteString(", body ")
			b.WriteString(op.RequestBody.AliasName)
			b.WriteString(", reqEditors ...RequestEditorFn) (*http.Response, error)\n\n")
		} else {
			b.WriteString("\t// ")
			b.WriteString(op.Name)
			b.WriteString(" request\n")
			b.WriteString("\t")
			b.WriteString(op.Name)
			b.WriteString("(ctx context.Context")
			for _, param := range op.PathParams {
				b.WriteString(", ")
				b.WriteString(param.VarName)
				b.WriteString(" ")
				b.WriteString(param.Type)
			}
			b.WriteString(", reqEditors ...RequestEditorFn) (*http.Response, error)\n\n")
		}
	}
	b.WriteString("}\n\n")

	for _, op := range doc.Operations {
		if op.RequestBody != nil {
			renderClientBodyMethod(b, op)
			renderClientJSONMethod(b, op)
		} else {
			renderClientNoBodyMethod(b, op)
		}
	}

	for _, op := range doc.Operations {
		if op.RequestBody != nil {
			renderNewRequestWithBody(b, op)
			renderNewRequestJSON(b, op)
		} else {
			renderNewRequestNoBody(b, op)
		}
	}

	b.WriteString("func (c *Client) applyEditors(ctx context.Context, req *http.Request, additionalEditors []RequestEditorFn) error {\n")
	b.WriteString("\tfor _, r := range c.RequestEditors {\n\t\tif err := r(ctx, req); err != nil {\n\t\t\treturn err\n\t\t}\n\t}\n")
	b.WriteString("\tfor _, r := range additionalEditors {\n\t\tif err := r(ctx, req); err != nil {\n\t\t\treturn err\n\t\t}\n\t}\n")
	b.WriteString("\treturn nil\n")
	b.WriteString("}\n\n")
	b.WriteString("// ClientWithResponses builds on ClientInterface to offer response payloads\n")
	b.WriteString("type ClientWithResponses struct {\n\tClientInterface\n}\n\n")
	b.WriteString("// NewClientWithResponses creates a new ClientWithResponses, which wraps\n")
	b.WriteString("// Client with return type handling\n")
	b.WriteString("func NewClientWithResponses(server string, opts ...ClientOption) (*ClientWithResponses, error) {\n")
	b.WriteString("\tclient, err := NewClient(server, opts...)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n")
	b.WriteString("\treturn &ClientWithResponses{client}, nil\n")
	b.WriteString("}\n\n")
	b.WriteString("// WithBaseURL overrides the baseURL.\n")
	b.WriteString("func WithBaseURL(baseURL string) ClientOption {\n")
	b.WriteString("\treturn func(c *Client) error {\n")
	b.WriteString("\t\tnewBaseURL, err := url.Parse(baseURL)\n\t\tif err != nil {\n\t\t\treturn err\n\t\t}\n")
	b.WriteString("\t\tc.Server = newBaseURL.String()\n\t\treturn nil\n\t}\n")
	b.WriteString("}\n\n")
	b.WriteString("// ClientWithResponsesInterface is the interface specification for the client with responses above.\n")
	b.WriteString("type ClientWithResponsesInterface interface {\n")
	for _, op := range doc.Operations {
		if op.RequestBody != nil {
			b.WriteString("\t// ")
			b.WriteString(op.Name)
			b.WriteString("WithBodyWithResponse request with any body\n")
			b.WriteString("\t")
			b.WriteString(op.Name)
			b.WriteString("WithBodyWithResponse(ctx context.Context")
			for _, param := range op.PathParams {
				b.WriteString(", ")
				b.WriteString(param.VarName)
				b.WriteString(" ")
				b.WriteString(param.Type)
			}
			b.WriteString(", contentType string, body io.Reader, reqEditors ...RequestEditorFn) (*")
			b.WriteString(op.Name)
			b.WriteString("Response, error)\n\n")
			b.WriteString("\t")
			b.WriteString(op.Name)
			b.WriteString("WithResponse(ctx context.Context")
			for _, param := range op.PathParams {
				b.WriteString(", ")
				b.WriteString(param.VarName)
				b.WriteString(" ")
				b.WriteString(param.Type)
			}
			b.WriteString(", body ")
			b.WriteString(op.RequestBody.AliasName)
			b.WriteString(", reqEditors ...RequestEditorFn) (*")
			b.WriteString(op.Name)
			b.WriteString("Response, error)\n\n")
		} else {
			b.WriteString("\t// ")
			b.WriteString(op.Name)
			b.WriteString("WithResponse request\n")
			b.WriteString("\t")
			b.WriteString(op.Name)
			b.WriteString("WithResponse(ctx context.Context")
			for _, param := range op.PathParams {
				b.WriteString(", ")
				b.WriteString(param.VarName)
				b.WriteString(" ")
				b.WriteString(param.Type)
			}
			b.WriteString(", reqEditors ...RequestEditorFn) (*")
			b.WriteString(op.Name)
			b.WriteString("Response, error)\n\n")
		}
	}
	b.WriteString("}\n\n")

	for _, op := range doc.Operations {
		renderResponseStruct(b, op)
	}
	for _, op := range doc.Operations {
		renderClientWithResponseMethod(b, op)
	}
	for _, op := range doc.Operations {
		renderParseResponse(b, op)
	}
}

func renderServer(b *strings.Builder, doc *document) {
	b.WriteString("// ServerInterface represents all server handlers.\n")
	b.WriteString("type ServerInterface interface {\n")
	for _, op := range doc.Operations {
		b.WriteString("\t// ")
		b.WriteString(op.Summary)
		b.WriteString("\n")
		b.WriteString("\t// (")
		b.WriteString(op.Method)
		b.WriteString(" ")
		b.WriteString(op.Path)
		b.WriteString(")\n")
		b.WriteString("\t")
		b.WriteString(op.Name)
		b.WriteString("(c *fiber.Ctx")
		for _, param := range op.PathParams {
			b.WriteString(", ")
			b.WriteString(param.VarName)
			b.WriteString(" ")
			b.WriteString(param.Type)
		}
		b.WriteString(") error\n")
	}
	b.WriteString("}\n\n")
	b.WriteString("// ServerInterfaceWrapper converts contexts to parameters.\n")
	b.WriteString("type ServerInterfaceWrapper struct {\n\tHandler ServerInterface\n}\n\n")
	b.WriteString("type MiddlewareFunc fiber.Handler\n\n")
	for _, op := range doc.Operations {
		b.WriteString("// ")
		b.WriteString(op.Name)
		b.WriteString(" operation middleware\n")
		b.WriteString("func (siw *ServerInterfaceWrapper) ")
		b.WriteString(op.Name)
		b.WriteString("(c *fiber.Ctx) error {\n")
		for _, param := range op.PathParams {
			renderPathParamParse(b, param)
		}
		for _, sec := range op.Securities {
			b.WriteString("\tc.Context().SetUserValue(")
			b.WriteString(sec.ConstName)
			b.WriteString(", []string{")
			for i, scope := range sec.Scopes {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(strconv.Quote(scope))
			}
			b.WriteString("})\n\n")
		}
		b.WriteString("\treturn siw.Handler.")
		b.WriteString(op.Name)
		b.WriteString("(c")
		for _, param := range op.PathParams {
			b.WriteString(", ")
			b.WriteString(param.VarName)
		}
		b.WriteString(")\n")
		b.WriteString("}\n\n")
	}
	b.WriteString("// FiberServerOptions provides options for the Fiber server.\n")
	b.WriteString("type FiberServerOptions struct {\n\tBaseURL string\n\tMiddlewares []MiddlewareFunc\n}\n\n")
	b.WriteString("// RegisterHandlers creates http.Handler with routing matching OpenAPI spec.\n")
	b.WriteString("func RegisterHandlers(router fiber.Router, si ServerInterface) {\n")
	b.WriteString("\tRegisterHandlersWithOptions(router, si, FiberServerOptions{})\n")
	b.WriteString("}\n\n")
	b.WriteString("// RegisterHandlersWithOptions creates http.Handler with additional options\n")
	b.WriteString("func RegisterHandlersWithOptions(router fiber.Router, si ServerInterface, options FiberServerOptions) {\n")
	b.WriteString("\twrapper := ServerInterfaceWrapper{Handler: si}\n\n")
	b.WriteString("\tfor _, m := range options.Middlewares {\n\t\trouter.Use(fiber.Handler(m))\n\t}\n\n")
	for _, op := range doc.Operations {
		b.WriteString("\trouter.")
		b.WriteString(routerMethodName(op.Method))
		b.WriteString("(options.BaseURL+")
		b.WriteString(strconv.Quote(op.FiberPath))
		b.WriteString(", wrapper.")
		b.WriteString(op.Name)
		b.WriteString(")\n\n")
	}
	b.WriteString("}\n")
}

func renderClientBodyMethod(b *strings.Builder, op operationDef) {
	b.WriteString("func (c *Client) ")
	b.WriteString(op.Name)
	b.WriteString("WithBody(ctx context.Context")
	for _, param := range op.PathParams {
		b.WriteString(", ")
		b.WriteString(param.VarName)
		b.WriteString(" ")
		b.WriteString(param.Type)
	}
	b.WriteString(", contentType string, body io.Reader, reqEditors ...RequestEditorFn) (*http.Response, error) {\n")
	b.WriteString("\treq, err := New")
	b.WriteString(op.Name)
	b.WriteString("RequestWithBody(c.Server")
	for _, param := range op.PathParams {
		b.WriteString(", ")
		b.WriteString(param.VarName)
	}
	b.WriteString(", contentType, body)\n")
	b.WriteString("\tif err != nil {\n\t\treturn nil, err\n\t}\n")
	b.WriteString("\treq = req.WithContext(ctx)\n")
	b.WriteString("\tif err := c.applyEditors(ctx, req, reqEditors); err != nil {\n\t\treturn nil, err\n\t}\n")
	b.WriteString("\treturn c.Client.Do(req)\n")
	b.WriteString("}\n\n")
}

func renderClientJSONMethod(b *strings.Builder, op operationDef) {
	b.WriteString("func (c *Client) ")
	b.WriteString(op.Name)
	b.WriteString("(ctx context.Context")
	for _, param := range op.PathParams {
		b.WriteString(", ")
		b.WriteString(param.VarName)
		b.WriteString(" ")
		b.WriteString(param.Type)
	}
	b.WriteString(", body ")
	b.WriteString(op.RequestBody.AliasName)
	b.WriteString(", reqEditors ...RequestEditorFn) (*http.Response, error) {\n")
	b.WriteString("\tbodyBytes, err := json.Marshal(body)\n")
	b.WriteString("\tif err != nil {\n\t\treturn nil, err\n\t}\n")
	b.WriteString("\treturn c.")
	b.WriteString(op.Name)
	b.WriteString("WithBody(ctx")
	for _, param := range op.PathParams {
		b.WriteString(", ")
		b.WriteString(param.VarName)
	}
	b.WriteString(", \"application/json\", bytes.NewReader(bodyBytes), reqEditors...)\n")
	b.WriteString("}\n\n")
}

func renderClientNoBodyMethod(b *strings.Builder, op operationDef) {
	b.WriteString("func (c *Client) ")
	b.WriteString(op.Name)
	b.WriteString("(ctx context.Context")
	for _, param := range op.PathParams {
		b.WriteString(", ")
		b.WriteString(param.VarName)
		b.WriteString(" ")
		b.WriteString(param.Type)
	}
	b.WriteString(", reqEditors ...RequestEditorFn) (*http.Response, error) {\n")
	b.WriteString("\treq, err := New")
	b.WriteString(op.Name)
	b.WriteString("Request(c.Server")
	for _, param := range op.PathParams {
		b.WriteString(", ")
		b.WriteString(param.VarName)
	}
	b.WriteString(")\n")
	b.WriteString("\tif err != nil {\n\t\treturn nil, err\n\t}\n")
	b.WriteString("\treq = req.WithContext(ctx)\n")
	b.WriteString("\tif err := c.applyEditors(ctx, req, reqEditors); err != nil {\n\t\treturn nil, err\n\t}\n")
	b.WriteString("\treturn c.Client.Do(req)\n")
	b.WriteString("}\n\n")
}

func renderNewRequestWithBody(b *strings.Builder, op operationDef) {
	b.WriteString("// New")
	b.WriteString(op.Name)
	b.WriteString("RequestWithBody generates requests for ")
	b.WriteString(op.Name)
	b.WriteString(" with any body\n")
	b.WriteString("func New")
	b.WriteString(op.Name)
	b.WriteString("RequestWithBody(server string")
	for _, param := range op.PathParams {
		b.WriteString(", ")
		b.WriteString(param.VarName)
		b.WriteString(" ")
		b.WriteString(param.Type)
	}
	b.WriteString(", contentType string, body io.Reader) (*http.Request, error) {\n")
	renderRequestBuilderBody(b, op, true)
	b.WriteString("}\n\n")
}

func renderNewRequestJSON(b *strings.Builder, op operationDef) {
	b.WriteString("// New")
	b.WriteString(op.Name)
	b.WriteString("Request generates requests for ")
	b.WriteString(op.Name)
	b.WriteString("\n")
	b.WriteString("func New")
	b.WriteString(op.Name)
	b.WriteString("Request(server string")
	for _, param := range op.PathParams {
		b.WriteString(", ")
		b.WriteString(param.VarName)
		b.WriteString(" ")
		b.WriteString(param.Type)
	}
	b.WriteString(", body ")
	b.WriteString(op.RequestBody.AliasName)
	b.WriteString(") (*http.Request, error) {\n")
	b.WriteString("\tbodyBytes, err := json.Marshal(body)\n")
	b.WriteString("\tif err != nil {\n\t\treturn nil, err\n\t}\n")
	b.WriteString("\treturn New")
	b.WriteString(op.Name)
	b.WriteString("RequestWithBody(server")
	for _, param := range op.PathParams {
		b.WriteString(", ")
		b.WriteString(param.VarName)
	}
	b.WriteString(", \"application/json\", bytes.NewReader(bodyBytes))\n")
	b.WriteString("}\n\n")
}

func renderNewRequestNoBody(b *strings.Builder, op operationDef) {
	b.WriteString("// New")
	b.WriteString(op.Name)
	b.WriteString("Request generates requests for ")
	b.WriteString(op.Name)
	b.WriteString("\n")
	b.WriteString("func New")
	b.WriteString(op.Name)
	b.WriteString("Request(server string")
	for _, param := range op.PathParams {
		b.WriteString(", ")
		b.WriteString(param.VarName)
		b.WriteString(" ")
		b.WriteString(param.Type)
	}
	b.WriteString(") (*http.Request, error) {\n")
	renderRequestBuilderBody(b, op, false)
	b.WriteString("}\n\n")
}

func renderRequestBuilderBody(b *strings.Builder, op operationDef, hasBody bool) {
	b.WriteString("\tserverURL, err := url.Parse(server)\n")
	b.WriteString("\tif err != nil {\n\t\treturn nil, err\n\t}\n\n")
	b.WriteString("\toperationPath := fmt.Sprintf(")
	b.WriteString(strconv.Quote(op.PathFormat))
	if op.PathArgs != "" {
		b.WriteString(", ")
		b.WriteString(op.PathArgs)
	}
	b.WriteString(")\n")
	b.WriteString("\tif operationPath[0] == '/' {\n\t\toperationPath = \".\" + operationPath\n\t}\n\n")
	b.WriteString("\tqueryURL, err := serverURL.Parse(operationPath)\n")
	b.WriteString("\tif err != nil {\n\t\treturn nil, err\n\t}\n\n")
	if hasBody {
		b.WriteString("\treq, err := http.NewRequest(")
		b.WriteString(strconv.Quote(op.Method))
		b.WriteString(", queryURL.String(), body)\n")
	} else {
		b.WriteString("\treq, err := http.NewRequest(")
		b.WriteString(strconv.Quote(op.Method))
		b.WriteString(", queryURL.String(), nil)\n")
	}
	b.WriteString("\tif err != nil {\n\t\treturn nil, err\n\t}\n")
	if hasBody {
		b.WriteString("\treq.Header.Set(\"Content-Type\", contentType)\n")
	}
	b.WriteString("\treturn req, nil\n")
}

func renderResponseStruct(b *strings.Builder, op operationDef) {
	b.WriteString("type ")
	b.WriteString(op.Name)
	b.WriteString("Response struct {\n\tBody []byte\n\tHTTPResponse *http.Response\n")
	for _, resp := range op.Responses {
		if resp.HasJSON {
			b.WriteString("\t")
			b.WriteString(resp.FieldName)
			b.WriteString(" *")
			b.WriteString(resp.GoType)
			b.WriteString("\n")
		}
	}
	b.WriteString("}\n\n")
	b.WriteString("// Status returns HTTPResponse.Status\n")
	b.WriteString("func (r ")
	b.WriteString(op.Name)
	b.WriteString("Response) Status() string {\n")
	b.WriteString("\tif r.HTTPResponse != nil {\n\t\treturn r.HTTPResponse.Status\n\t}\n")
	b.WriteString("\treturn http.StatusText(0)\n")
	b.WriteString("}\n\n")
	b.WriteString("// StatusCode returns HTTPResponse.StatusCode\n")
	b.WriteString("func (r ")
	b.WriteString(op.Name)
	b.WriteString("Response) StatusCode() int {\n")
	b.WriteString("\tif r.HTTPResponse != nil {\n\t\treturn r.HTTPResponse.StatusCode\n\t}\n")
	b.WriteString("\treturn 0\n")
	b.WriteString("}\n\n")
}

func renderClientWithResponseMethod(b *strings.Builder, op operationDef) {
	if op.RequestBody != nil {
		b.WriteString("// ")
		b.WriteString(op.Name)
		b.WriteString("WithBodyWithResponse request with arbitrary body returning *")
		b.WriteString(op.Name)
		b.WriteString("Response\n")
		b.WriteString("func (c *ClientWithResponses) ")
		b.WriteString(op.Name)
		b.WriteString("WithBodyWithResponse(ctx context.Context")
		for _, param := range op.PathParams {
			b.WriteString(", ")
			b.WriteString(param.VarName)
			b.WriteString(" ")
			b.WriteString(param.Type)
		}
		b.WriteString(", contentType string, body io.Reader, reqEditors ...RequestEditorFn) (*")
		b.WriteString(op.Name)
		b.WriteString("Response, error) {\n")
		b.WriteString("\trsp, err := c.")
		b.WriteString(op.Name)
		b.WriteString("WithBody(ctx")
		for _, param := range op.PathParams {
			b.WriteString(", ")
			b.WriteString(param.VarName)
		}
		b.WriteString(", contentType, body, reqEditors...)\n")
		b.WriteString("\tif err != nil {\n\t\treturn nil, err\n\t}\n")
		b.WriteString("\treturn Parse")
		b.WriteString(op.Name)
		b.WriteString("Response(rsp)\n")
		b.WriteString("}\n\n")

		b.WriteString("func (c *ClientWithResponses) ")
		b.WriteString(op.Name)
		b.WriteString("WithResponse(ctx context.Context")
		for _, param := range op.PathParams {
			b.WriteString(", ")
			b.WriteString(param.VarName)
			b.WriteString(" ")
			b.WriteString(param.Type)
		}
		b.WriteString(", body ")
		b.WriteString(op.RequestBody.AliasName)
		b.WriteString(", reqEditors ...RequestEditorFn) (*")
		b.WriteString(op.Name)
		b.WriteString("Response, error) {\n")
		b.WriteString("\trsp, err := c.")
		b.WriteString(op.Name)
		b.WriteString("(ctx")
		for _, param := range op.PathParams {
			b.WriteString(", ")
			b.WriteString(param.VarName)
		}
		b.WriteString(", body, reqEditors...)\n")
		b.WriteString("\tif err != nil {\n\t\treturn nil, err\n\t}\n")
		b.WriteString("\treturn Parse")
		b.WriteString(op.Name)
		b.WriteString("Response(rsp)\n")
		b.WriteString("}\n\n")
		return
	}

	b.WriteString("// ")
	b.WriteString(op.Name)
	b.WriteString("WithResponse request returning *")
	b.WriteString(op.Name)
	b.WriteString("Response\n")
	b.WriteString("func (c *ClientWithResponses) ")
	b.WriteString(op.Name)
	b.WriteString("WithResponse(ctx context.Context")
	for _, param := range op.PathParams {
		b.WriteString(", ")
		b.WriteString(param.VarName)
		b.WriteString(" ")
		b.WriteString(param.Type)
	}
	b.WriteString(", reqEditors ...RequestEditorFn) (*")
	b.WriteString(op.Name)
	b.WriteString("Response, error) {\n")
	b.WriteString("\trsp, err := c.")
	b.WriteString(op.Name)
	b.WriteString("(ctx")
	for _, param := range op.PathParams {
		b.WriteString(", ")
		b.WriteString(param.VarName)
	}
	b.WriteString(", reqEditors...)\n")
	b.WriteString("\tif err != nil {\n\t\treturn nil, err\n\t}\n")
	b.WriteString("\treturn Parse")
	b.WriteString(op.Name)
	b.WriteString("Response(rsp)\n")
	b.WriteString("}\n\n")
}

func renderParseResponse(b *strings.Builder, op operationDef) {
	b.WriteString("// Parse")
	b.WriteString(op.Name)
	b.WriteString("Response parses an HTTP response from a ")
	b.WriteString(op.Name)
	b.WriteString("WithResponse call\n")
	b.WriteString("func Parse")
	b.WriteString(op.Name)
	b.WriteString("Response(rsp *http.Response) (*")
	b.WriteString(op.Name)
	b.WriteString("Response, error) {\n")
	b.WriteString("\tbodyBytes, err := io.ReadAll(rsp.Body)\n")
	b.WriteString("\tdefer func() { _ = rsp.Body.Close() }()\n")
	b.WriteString("\tif err != nil {\n\t\treturn nil, err\n\t}\n\n")
	b.WriteString("\tresponse := &")
	b.WriteString(op.Name)
	b.WriteString("Response{Body: bodyBytes, HTTPResponse: rsp}\n\n")
	if hasJSONResponse(op) {
		b.WriteString("\tswitch {\n")
		for _, resp := range op.Responses {
			if !resp.HasJSON {
				continue
			}
			b.WriteString("\tcase strings.Contains(rsp.Header.Get(\"Content-Type\"), \"json\") && rsp.StatusCode == ")
			b.WriteString(strconv.Itoa(resp.StatusCode))
			b.WriteString(":\n")
			b.WriteString("\t\tvar dest ")
			b.WriteString(resp.GoType)
			b.WriteString("\n")
			b.WriteString("\t\tif err := json.Unmarshal(bodyBytes, &dest); err != nil {\n\t\t\treturn nil, err\n\t\t}\n")
			b.WriteString("\t\tresponse.")
			b.WriteString(resp.FieldName)
			b.WriteString(" = &dest\n")
		}
		b.WriteString("\t}\n\n")
	}
	b.WriteString("\treturn response, nil\n")
	b.WriteString("}\n\n")
}

func renderPathParamParse(b *strings.Builder, param paramDef) {
	b.WriteString("\tvar ")
	b.WriteString(param.VarName)
	b.WriteString(" ")
	b.WriteString(param.Type)
	b.WriteString("\n")
	switch param.Type {
	case "string":
		b.WriteString("\t")
		b.WriteString(param.VarName)
		b.WriteString(" = c.Params(")
		b.WriteString(strconv.Quote(param.SourceName))
		b.WriteString(")\n\n")
	case "int32":
		b.WriteString("\tparsed")
		b.WriteString(param.GoName)
		b.WriteString(", err := strconv.ParseInt(c.Params(")
		b.WriteString(strconv.Quote(param.SourceName))
		b.WriteString("), 10, 32)\n")
		b.WriteString("\tif err != nil {\n\t\treturn fiber.NewError(fiber.StatusBadRequest, fmt.Errorf(\"Invalid format for parameter ")
		b.WriteString(param.SourceName)
		b.WriteString(": %w\", err).Error())\n\t}\n")
		b.WriteString("\t")
		b.WriteString(param.VarName)
		b.WriteString(" = int32(parsed")
		b.WriteString(param.GoName)
		b.WriteString(")\n\n")
	case "int64":
		b.WriteString("\t")
		b.WriteString(param.VarName)
		b.WriteString(", err := strconv.ParseInt(c.Params(")
		b.WriteString(strconv.Quote(param.SourceName))
		b.WriteString("), 10, 64)\n")
		b.WriteString("\tif err != nil {\n\t\treturn fiber.NewError(fiber.StatusBadRequest, fmt.Errorf(\"Invalid format for parameter ")
		b.WriteString(param.SourceName)
		b.WriteString(": %w\", err).Error())\n\t}\n\n")
	default:
		b.WriteString("\t")
		b.WriteString(param.VarName)
		b.WriteString("Value := c.Params(")
		b.WriteString(strconv.Quote(param.SourceName))
		b.WriteString(")\n")
		b.WriteString("\t_ = ")
		b.WriteString(param.VarName)
		b.WriteString("Value\n\n")
	}
}

func specImports(doc *document) []string {
	imports := map[string]struct{}{
		"context":                     {},
		"fmt":                         {},
		"io":                          {},
		"net/http":                    {},
		"net/url":                     {},
		"strings":                     {},
		"github.com/gofiber/fiber/v2": {},
	}
	for imp := range doc.SpecTypeImports {
		imports[imp] = struct{}{}
	}
	if specNeedsJSON(doc) {
		imports["encoding/json"] = struct{}{}
	}
	if specNeedsBytes(doc) {
		imports["bytes"] = struct{}{}
	}
	if specNeedsStrconv(doc) {
		imports["strconv"] = struct{}{}
	}
	return sortedImports(imports)
}

func scopeImports(doc *document) []string {
	imports := map[string]struct{}{
		"github.com/gofiber/fiber/v2": {},
	}
	for imp := range doc.ScopeTypeImports {
		imports[imp] = struct{}{}
	}
	if scopeNeedsContext(doc) {
		imports["context"] = struct{}{}
	}
	return sortedImports(imports)
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

func writeFile(workdir, path, content string) error {
	fullPath := path
	if !filepath.IsAbs(fullPath) {
		fullPath = filepath.Join(workdir, fullPath)
	}
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return err
	}
	return os.WriteFile(fullPath, []byte(content), 0644)
}

func defaultMiddlewareOut(out string) string {
	return filepath.Join(filepath.Dir(out), "scopes_extend_gen.go")
}

func parseXCheckRules(doc *document, spec *openapi3.T, enumMap map[string]*enumDef) error {
	if spec.Extensions == nil {
		return nil
	}
	raw, ok := spec.Extensions["x-check-rules"]
	if !ok {
		return nil
	}
	payload, ok := raw.(map[string]any)
	if !ok {
		return errors.New("x-check-rules is not a map")
	}
	parsed := map[string]rawCheckRule{}
	if err := jsonParse(payload, &parsed); err != nil {
		return err
	}
	for name, rule := range parsed {
		item := xCheckRule{Name: name, UseContext: rule.UseContext, Description: rule.Description}
		for _, param := range rule.Parameters {
			resolved, err := resolveType(param.Schema, name+exportName(param.Name), enumMap)
			if err != nil {
				return err
			}
			mergeImports(doc.ScopeTypeImports, resolved.Imports...)
			item.Params = append(item.Params, xParam{Name: lowerName(param.Name), Description: param.Description, Type: resolved.GoType})
		}
		doc.CheckRules = append(doc.CheckRules, item)
	}
	return nil
}

func parseXFunctions(doc *document, spec *openapi3.T, enumMap map[string]*enumDef) error {
	if spec.Extensions == nil {
		return nil
	}
	raw, ok := spec.Extensions["x-functions"]
	if !ok {
		return nil
	}
	payload, ok := raw.(map[string]any)
	if !ok {
		return errors.New("x-functions is not a map")
	}
	parsed := map[string]rawFunction{}
	if err := jsonParse(payload, &parsed); err != nil {
		return err
	}
	for name, fn := range parsed {
		item := xFunction{Name: name, UseContext: fn.UseContext, Description: fn.Description}
		for _, param := range fn.Params {
			resolved, err := resolveType(param.Schema, name+exportName(param.Name), enumMap)
			if err != nil {
				return err
			}
			mergeImports(doc.ScopeTypeImports, resolved.Imports...)
			item.Params = append(item.Params, xParam{Name: lowerName(param.Name), Description: param.Description, Type: resolved.GoType})
		}
		resolved, err := resolveType(fn.Return.Schema, name+"Result", enumMap)
		if err != nil {
			return err
		}
		mergeImports(doc.ScopeTypeImports, resolved.Imports...)
		item.Return = xParam{Name: lowerName(fn.Return.Name), Description: fn.Return.Description, Type: resolved.GoType}
		doc.Functions = append(doc.Functions, item)
	}
	return nil
}

func resolveType(ref *openapi3.SchemaRef, hint string, enumMap map[string]*enumDef) (resolvedType, error) {
	if ref == nil || ref.Value == nil {
		return resolvedType{GoType: "interface{}"}, nil
	}
	if ref.Ref != "" {
		if name, ok := componentNameFromRef(ref.Ref); ok {
			return resolvedType{GoType: exportName(name)}, nil
		}
	}

	schema := ref.Value
	if customType, customImports := customGoType(schema); customType != "" {
		return resolvedType{GoType: customType, Imports: customImports}, nil
	}

	if len(schema.Enum) > 0 {
		baseType := primitiveType(schema)
		enumName := exportName(hint)
		if enumName == "" {
			return resolvedType{GoType: baseType}, nil
		}
		ensureEnum(enumMap, enumName, baseType, schema.Description, schema.Enum)
		return resolvedType{GoType: enumName}, nil
	}

	if schema.Type == nil {
		return resolvedType{GoType: "interface{}"}, nil
	}
	switch {
	case schema.Type.Is("string"):
		if schema.Format == "date-time" {
			return resolvedType{GoType: "time.Time", Imports: []string{"time"}}, nil
		}
		return resolvedType{GoType: "string"}, nil
	case schema.Type.Is("integer"):
		switch schema.Format {
		case "int32":
			return resolvedType{GoType: "int32"}, nil
		case "int64":
			return resolvedType{GoType: "int64"}, nil
		default:
			return resolvedType{GoType: "int"}, nil
		}
	case schema.Type.Is("number"):
		switch schema.Format {
		case "float":
			return resolvedType{GoType: "float32"}, nil
		default:
			return resolvedType{GoType: "float64"}, nil
		}
	case schema.Type.Is("boolean"):
		return resolvedType{GoType: "bool"}, nil
	case schema.Type.Is("array"):
		item, err := resolveType(schema.Items, hint, enumMap)
		if err != nil {
			return resolvedType{}, err
		}
		return resolvedType{GoType: "[]" + item.GoType, Imports: item.Imports}, nil
	case schema.Type.Is("object"):
		return resolvedType{GoType: "map[string]any"}, nil
	default:
		return resolvedType{GoType: "interface{}"}, nil
	}
}

func ensureEnum(enumMap map[string]*enumDef, name, baseType, description string, values []any) {
	if _, ok := enumMap[name]; ok {
		return
	}
	enum := &enumDef{Name: name, Type: baseType, Description: description}
	for _, value := range values {
		enum.Values = append(enum.Values, enumValue{Name: exportName(fmt.Sprint(value)), Literal: enumLiteral(baseType, value)})
	}
	enumMap[name] = enum
}

func primitiveType(schema *openapi3.Schema) string {
	if schema == nil || schema.Type == nil {
		return "interface{}"
	}
	switch {
	case schema.Type.Is("string"):
		return "string"
	case schema.Type.Is("integer"):
		switch schema.Format {
		case "int32":
			return "int32"
		case "int64":
			return "int64"
		default:
			return "int"
		}
	case schema.Type.Is("number"):
		switch schema.Format {
		case "float":
			return "float32"
		default:
			return "float64"
		}
	case schema.Type.Is("boolean"):
		return "bool"
	default:
		return "string"
	}
}

func customGoType(schema *openapi3.Schema) (string, []string) {
	if schema == nil || schema.Extensions == nil {
		return "", nil
	}
	value, ok := schema.Extensions["x-go-type"]
	if !ok {
		return "", nil
	}
	goType, ok := value.(string)
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
		case []string:
			imports = append(imports, typed...)
		}
	}
	return goType, imports
}

func orderedOperations(pathItem *openapi3.PathItem) []struct {
	method    string
	operation *openapi3.Operation
} {
	ret := make([]struct {
		method    string
		operation *openapi3.Operation
	}, 0, 8)
	appendOp := func(method string, op *openapi3.Operation) {
		if op != nil {
			ret = append(ret, struct {
				method    string
				operation *openapi3.Operation
			}{method: method, operation: op})
		}
	}
	appendOp("GET", pathItem.Get)
	appendOp("POST", pathItem.Post)
	appendOp("PUT", pathItem.Put)
	appendOp("PATCH", pathItem.Patch)
	appendOp("DELETE", pathItem.Delete)
	appendOp("OPTIONS", pathItem.Options)
	appendOp("HEAD", pathItem.Head)
	appendOp("TRACE", pathItem.Trace)
	return ret
}

func collectPathParams(path string, pathItem *openapi3.PathItem, op *openapi3.Operation) ([]paramDef, error) {
	names := pathParamNames(path)
	if len(names) == 0 {
		return nil, nil
	}
	paramMap := map[string]*openapi3.Parameter{}
	for _, paramRef := range pathItem.Parameters {
		if paramRef != nil && paramRef.Value != nil && paramRef.Value.In == "path" {
			paramMap[paramRef.Value.Name] = paramRef.Value
		}
	}
	for _, paramRef := range op.Parameters {
		if paramRef != nil && paramRef.Value != nil && paramRef.Value.In == "path" {
			paramMap[paramRef.Value.Name] = paramRef.Value
		}
	}
	ret := make([]paramDef, 0, len(names))
	for _, name := range names {
		param := paramMap[name]
		if param == nil {
			return nil, errors.Errorf("missing path parameter %s", name)
		}
		resolved, err := resolveType(param.Schema, exportName(name), map[string]*enumDef{})
		if err != nil {
			return nil, err
		}
		ret = append(ret, paramDef{
			Name:       exportName(name),
			VarName:    lowerName(name),
			GoName:     exportName(name),
			Type:       resolved.GoType,
			SourceName: name,
		})
	}
	return ret, nil
}

func buildPathFormatter(path string, params []paramDef) (string, string) {
	if len(params) == 0 {
		return path, ""
	}
	format := path
	args := make([]string, 0, len(params))
	for _, param := range params {
		format = strings.ReplaceAll(format, "{"+param.SourceName+"}", "%v")
		args = append(args, param.VarName)
	}
	return format, strings.Join(args, ", ")
}

func jsonBodySchema(content openapi3.Content) (string, *openapi3.SchemaRef, bool) {
	for contentType, mediaType := range content {
		if mediaType != nil && strings.Contains(contentType, "json") {
			return contentType, mediaType.Schema, true
		}
	}
	return "", nil, false
}

func jsonResponseSchema(content openapi3.Content) (string, *openapi3.SchemaRef, bool) {
	for contentType, mediaType := range content {
		if mediaType != nil && strings.Contains(contentType, "json") {
			return contentType, mediaType.Schema, true
		}
	}
	return "", nil, false
}

func responseSortKey(code string) int {
	if code == "default" {
		return 999
	}
	value, err := strconv.Atoi(code)
	if err != nil {
		return 999
	}
	return value
}

func hasJSONResponse(op operationDef) bool {
	for _, resp := range op.Responses {
		if resp.HasJSON {
			return true
		}
	}
	return false
}

func specNeedsJSON(doc *document) bool {
	if _, ok := doc.SpecTypeImports["encoding/json"]; ok {
		return true
	}
	for _, op := range doc.Operations {
		if op.RequestBody != nil {
			return true
		}
		if hasJSONResponse(op) {
			return true
		}
	}
	return false
}

func specNeedsBytes(doc *document) bool {
	for _, op := range doc.Operations {
		if op.RequestBody != nil {
			return true
		}
	}
	return false
}

func specNeedsStrconv(doc *document) bool {
	for _, op := range doc.Operations {
		for _, param := range op.PathParams {
			if param.Type != "string" {
				return true
			}
		}
	}
	return false
}

func scopeNeedsContext(doc *document) bool {
	for _, rule := range doc.CheckRules {
		if !rule.UseContext {
			return true
		}
	}
	for _, fn := range doc.Functions {
		if !fn.UseContext {
			return true
		}
	}
	return false
}

func scopeReceiver(useContext bool) string {
	if useContext {
		return "c *fiber.Ctx"
	}
	return "ctx context.Context"
}

func operationNeedsOperationID(op operationDef) bool {
	for _, sec := range op.Securities {
		for _, scope := range sec.Scopes {
			if strings.Contains(scope, "operationID") {
				return true
			}
		}
	}
	return false
}

func mergeImports(target map[string]struct{}, imports ...string) {
	for _, imp := range imports {
		if imp == "" {
			continue
		}
		target[imp] = struct{}{}
	}
}

func sortedImports(imports map[string]struct{}) []string {
	ret := make([]string, 0, len(imports))
	for imp := range imports {
		ret = append(ret, imp)
	}
	sort.Strings(ret)
	return ret
}

func componentNameFromRef(ref string) (string, bool) {
	const prefix = "#/components/schemas/"
	if !strings.HasPrefix(ref, prefix) {
		return "", false
	}
	return strings.TrimPrefix(ref, prefix), true
}

func requiredSet(items []string) map[string]bool {
	ret := make(map[string]bool, len(items))
	for _, item := range items {
		ret[item] = true
	}
	return ret
}

func pointerType(goType string) string {
	if strings.HasPrefix(goType, "*") {
		return goType
	}
	return "*" + goType
}

func enumLiteral(goType string, value any) string {
	switch goType {
	case "string":
		return strconv.Quote(fmt.Sprint(value))
	default:
		return fmt.Sprint(value)
	}
}

func jsonTag(name string, optional bool) string {
	if optional {
		return name + ",omitempty"
	}
	return name
}

func writeComment(b *strings.Builder, text, indent string) {
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

var wordBoundary = regexp.MustCompile(`[^a-zA-Z0-9]+`)

func exportName(value string) string {
	words := splitWords(value)
	if len(words) == 0 {
		return ""
	}
	for i, word := range words {
		if word == strings.ToUpper(word) {
			words[i] = word
			continue
		}
		words[i] = strings.ToUpper(word[:1]) + word[1:]
	}
	return strings.Join(words, "")
}

func lowerName(value string) string {
	words := splitWords(value)
	if len(words) == 0 {
		return ""
	}
	for i, word := range words {
		if word == strings.ToUpper(word) {
			words[i] = word
			continue
		}
		if i == 0 {
			words[i] = strings.ToLower(word[:1]) + word[1:]
		} else {
			words[i] = strings.ToUpper(word[:1]) + word[1:]
		}
	}
	return strings.Join(words, "")
}

func splitWords(value string) []string {
	if value == "" {
		return nil
	}
	normalized := wordBoundary.ReplaceAllString(value, " ")
	parts := strings.Fields(normalized)
	var words []string
	for _, part := range parts {
		start := 0
		runes := []rune(part)
		for i := 1; i < len(runes); i++ {
			prev := runes[i-1]
			curr := runes[i]
			nextLower := i+1 < len(runes) && isLower(runes[i+1])
			if isUpper(curr) && (isLower(prev) || (isUpper(prev) && nextLower)) {
				words = append(words, string(runes[start:i]))
				start = i
			}
		}
		words = append(words, string(runes[start:]))
	}
	return words
}

func isUpper(r rune) bool { return r >= 'A' && r <= 'Z' }
func isLower(r rune) bool { return r >= 'a' && r <= 'z' }

func fiberPath(path string) string {
	return regexp.MustCompile(`\{([^}]+)\}`).ReplaceAllString(path, `:$1`)
}

func routerMethodName(method string) string {
	switch strings.ToUpper(method) {
	case "GET":
		return "Get"
	case "POST":
		return "Post"
	case "PUT":
		return "Put"
	case "PATCH":
		return "Patch"
	case "DELETE":
		return "Delete"
	case "OPTIONS":
		return "Options"
	case "HEAD":
		return "Head"
	case "TRACE":
		return "Trace"
	default:
		return exportName(strings.ToLower(method))
	}
}

func pathParamNames(path string) []string {
	matches := regexp.MustCompile(`\{([^}]+)\}`).FindAllStringSubmatch(path, -1)
	ret := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) == 2 {
			ret = append(ret, match[1])
		}
	}
	return ret
}

func jsonParse[T any](raw map[string]any, out *T) error {
	buf, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(buf, out)
}

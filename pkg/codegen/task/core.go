package codegen

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/cloudcarver/anclax/pkg/codegen/gotypes"
	schema_codegen "github.com/cloudcarver/anclax/pkg/codegen/schemas"
	"github.com/cloudcarver/anclax/pkg/utils"
	"github.com/getkin/kin-openapi/openapi3"
	"gopkg.in/yaml.v3"
)

var globalTypeNameCounter = map[string]int{}

type paramSpec struct {
	Type      string
	StructDef string
	Imports   []string
}

func resetGlobalTypeNameCounter() {
	globalTypeNameCounter = map[string]int{}
}

func process(data map[string]any, onFunc func(f Function) error, onParam func(name string, params map[string]any) (paramSpec, error)) error {
	for k := range data {
		if k != "tasks" {
			log.Default().Printf("[WARN] tool type %s is not supported. Skipped.", k)
		}
	}

	tasks, ok := data["tasks"].([]any)
	if !ok {
		return errors.New("tasks is not an array")
	}

	for _, fn := range tasks {
		fnData, ok := fn.(map[string]any)
		if !ok {
			return errors.New("function cannot be parsed to a map")
		}

		fnName, ok := fnData["name"].(string)
		if !ok {
			return errors.New("function name cannot be parsed to a string")
		}

		var description string

		if _, ok := fnData["description"]; ok {
			description, ok = fnData["description"].(string)
			if !ok {
				return errors.New("function description cannot be parsed to a string")
			}
		}

		// parse delay
		var delay *string
		if _, ok := fnData["delay"]; ok {
			delayStr, ok := fnData["delay"].(string)
			if !ok {
				return errors.New("delay cannot be parsed to a string")
			}
			_, err := time.ParseDuration(delayStr)
			if err != nil {
				return errors.New("delay is not a valid duration, e.g. 1h, 1d, 1m")
			}
			delay = &delayStr
		}

		// parse timeout
		var timeout *string
		if _, ok := fnData["timeout"]; ok {
			timeoutStr, ok := fnData["timeout"].(string)
			if !ok {
				return errors.New("timeout cannot be parsed to a string")
			}
			_, err := time.ParseDuration(timeoutStr)
			if err != nil {
				return errors.New("timeout should be a valid duration, e.g. 1h, 1d, 1m")
			}
			timeout = &timeoutStr
		}

		// parse cronjob
		var cronjob *Cronjob
		if _, ok := fnData["cronjob"]; ok {
			cronjobStr, ok := fnData["cronjob"].(map[string]any)
			if !ok {
				return errors.New("cronjob cannot be parsed to a map")
			}
			cronjob = &Cronjob{
				CronExpression: cronjobStr["cronExpression"].(string),
			}
		}

		// parse retry policy
		var retryPolicy *RetryPolicy
		if _, ok := fnData["retryPolicy"]; ok {
			retryPolicyStr, ok := fnData["retryPolicy"].(map[string]any)
			if !ok {
				return errors.New("retryPolicy cannot be parsed to a map")
			}
			interval, ok := retryPolicyStr["interval"].(string)
			if !ok {
				return fmt.Errorf("interval %v cannot be parsed to a string", retryPolicyStr["interval"])
			}
			maxAttempts, ok := retryPolicyStr["maxAttempts"].(int)
			if !ok {
				return fmt.Errorf("maxAttempts %v cannot be parsed to a integer in %s: %v", retryPolicyStr["maxAttempts"], fnName, retryPolicyStr)
			}
			retryPolicy = &RetryPolicy{
				Interval:    interval,
				MaxAttempts: int32(maxAttempts),
			}
		}

		// parse events
		var events *Events
		if _, ok := fnData["events"]; ok {
			eventsData, ok := fnData["events"].([]any)
			if !ok {
				return errors.New("events cannot be parsed to an array")
			}
			events = &Events{}
			for _, event := range eventsData {
				eventStr, ok := event.(string)
				if !ok {
					return errors.New("event cannot be parsed to a string")
				}
				if eventStr == "onFailed" {
					events.OnFailed = &eventStr
				}
			}
		}

		// parse labels
		var labels []string
		if _, ok := fnData["labels"]; ok {
			labelsData, ok := fnData["labels"].([]any)
			if !ok {
				return errors.New("labels cannot be parsed to an array")
			}
			for _, label := range labelsData {
				labelStr, ok := label.(string)
				if !ok {
					return errors.New("label cannot be parsed to a string")
				}
				labels = append(labels, labelStr)
			}
		}

		// parse tags
		var tags []string
		if _, ok := fnData["tags"]; ok {
			tagsData, ok := fnData["tags"].([]any)
			if !ok {
				return errors.New("tags cannot be parsed to an array")
			}
			for _, tag := range tagsData {
				tagStr, ok := tag.(string)
				if !ok {
					return errors.New("tag cannot be parsed to a string")
				}
				tags = append(tags, tagStr)
			}
		}

		// parse priority
		var priority *int32
		if rawPriority, ok := fnData["priority"]; ok {
			parsedPriority, err := parseTaskPriority(rawPriority)
			if err != nil {
				return fmt.Errorf("priority for %s: %w", fnName, err)
			}
			priority = parsedPriority
		}

		// parse parameters (optional)
		structName := addGlobalType(fmt.Sprintf("%sParameters", utils.UpperFirst(fnName)))
		var (
			parameterInfo paramSpec
			err           error
		)
		if _, ok := fnData["parameters"]; ok {
			parameters, ok := fnData["parameters"].(map[string]any)
			if !ok {
				return errors.New("parameters cannot be parsed to a map")
			}
			parameterInfo, err = onParam(structName, parameters)
			if err != nil {
				return err
			}
		} else {
			defaultParams := map[string]any{
				"type":     "object",
				"required": []any{"taskID"},
				"properties": map[string]any{
					"taskID": map[string]any{
						"type":        "integer",
						"format":      "int32",
						"description": "The ID of the task that triggered this event",
					},
				},
			}
			parameterInfo, err = onParam(structName, defaultParams)
			if err != nil {
				return err
			}
		}

		if err := onFunc(Function{
			Name:            fnName,
			Description:     description,
			ParameterType:   parameterInfo.Type,
			Timeout:         timeout,
			Cronjob:         cronjob,
			RetryPolicy:     retryPolicy,
			Delay:           delay,
			Events:          events,
			Labels:          labels,
			Tags:            tags,
			Priority:        priority,
			HasLocalHelpers: parameterInfo.StructDef != "",
		}); err != nil {
			return err
		}
	}
	return nil
}

func parseTaskPriority(raw any) (*int32, error) {
	switch value := raw.(type) {
	case int:
		return parseTaskPriorityInt64(int64(value))
	case int32:
		return parseTaskPriorityInt64(int64(value))
	case int64:
		return parseTaskPriorityInt64(value)
	case uint64:
		if value > math.MaxInt32 {
			return nil, fmt.Errorf("priority %d exceeds max int32", value)
		}
		return parseTaskPriorityInt64(int64(value))
	case float64:
		if math.IsInf(value, 0) {
			max := int32(math.MaxInt32)
			return &max, nil
		}
		if math.IsNaN(value) {
			return nil, errors.New("priority must be int32 or INF")
		}
		if value != math.Trunc(value) {
			return nil, errors.New("priority must be an integer")
		}
		return parseTaskPriorityInt64(int64(value))
	case string:
		trimmed := strings.TrimSpace(value)
		if strings.EqualFold(trimmed, "INF") {
			max := int32(math.MaxInt32)
			return &max, nil
		}
		parsed, err := strconv.ParseInt(trimmed, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("priority %q must be int32 or INF", value)
		}
		return parseTaskPriorityInt64(parsed)
	default:
		return nil, fmt.Errorf("priority %v must be int32 or INF", raw)
	}
}

func parseTaskPriorityInt64(value int64) (*int32, error) {
	if value < 0 {
		return nil, errors.New("priority must be non-negative")
	}
	if value > math.MaxInt32 {
		return nil, fmt.Errorf("priority %d exceeds max int32", value)
	}
	priority := int32(value)
	return &priority, nil
}

func descriptionToComment(description string) string {
	description = strings.Trim(description, " \n\t\r")
	var rtn = ""
	var arr = strings.Split(description, "\n")
	for i, line := range arr {
		rtn += "// " + line
		if i != len(arr)-1 {
			rtn += "\n"
		}
	}
	return indent(rtn, 4)
}

func indent(s string, spaces int) string {
	indent := strings.Repeat(" ", spaces)
	return indent + strings.ReplaceAll(s, "\n", "\n"+indent)
}

func Generate(workdir, packageName, taskDefPath, outPath string, schemaConfig *schema_codegen.Config) error {
	raw, err := os.ReadFile(filepath.Join(workdir, taskDefPath))
	if err != nil {
		return err
	}
	raw = schema_codegen.NormalizeRefBytes(raw)
	var data map[string]any
	if err := yaml.Unmarshal(raw, &data); err != nil {
		return err
	}

	resetGlobalTypeNameCounter()

	schemaManager, err := schema_codegen.Load(workdir, derefSchemaConfig(schemaConfig))
	if err != nil {
		return err
	}

	result, err := generateToolInterfaces(workdir, packageName, filepath.Join(workdir, taskDefPath), data, schemaManager)
	if err != nil {
		return err
	}

	if err := os.WriteFile(filepath.Join(workdir, outPath), []byte(result), 0644); err != nil {
		return err
	}

	return nil
}

func generateToolInterfaces(workdir, packageName, taskDefFile string, data map[string]any, schemaManager *schema_codegen.Manager) (string, error) {
	var structDef string
	functions := []Function{}
	importSet := map[string]struct{}{}

	onFunc := func(f Function) error {
		functions = append(functions, f)
		return nil
	}

	onParam := func(name string, params map[string]any) (paramSpec, error) {
		ref, err := schema_codegen.UnmarshalSchemaRef(params)
		if err != nil {
			return paramSpec{}, err
		}
		spec, err := resolveParamSpec(taskDefFile, name, ref, schemaManager)
		if err != nil {
			return paramSpec{}, err
		}
		if spec.StructDef != "" {
			structDef += spec.StructDef + "\n"
		}
		for _, imp := range spec.Imports {
			importSet[imp] = struct{}{}
		}
		return spec, nil
	}

	tcTemplate, err := template.New("file").Funcs(template.FuncMap{
		"upperFirst": utils.UpperFirst,
		"derefInt32": func(v *int32) int32 {
			if v == nil {
				return 0
			}
			return *v
		},
	}).Parse(codeFileTemplate)
	if err != nil {
		return "", err
	}

	if err := process(data, onFunc, onParam); err != nil {
		return "", err
	}

	for i := range functions {
		functions[i].Description = descriptionToComment(functions[i].Description)
	}

	buf := bytes.NewBuffer([]byte{})
	if err := tcTemplate.Execute(buf, CodeTemplateVars{
		PackageName: packageName,
		StructDefs:  structDef,
		Functions:   functions,
		Imports:     sortedImportSlice(importSet),
	}); err != nil {
		return "", err
	}

	return buf.String(), nil
}

func addGlobalType(name string) string {
	if _, ok := globalTypeNameCounter[name]; ok {
		globalTypeNameCounter[name]++
		return fmt.Sprintf("%s%d", name, globalTypeNameCounter[name])
	} else {
		globalTypeNameCounter[name] = 0
		return name
	}
}

func resolveParamSpec(currentFile, name string, ref *openapi3.SchemaRef, schemaManager *schema_codegen.Manager) (paramSpec, error) {
	imports := map[string]struct{}{}
	goType, structDef, err := parseSchemaToType(currentFile, name, ref, schemaManager, imports)
	if err != nil {
		return paramSpec{}, err
	}
	return paramSpec{Type: goType, StructDef: structDef, Imports: sortedImportSlice(imports)}, nil
}

func parseSchemaToType(currentFile, typeName string, ref *openapi3.SchemaRef, schemaManager *schema_codegen.Manager, imports map[string]struct{}) (string, string, error) {
	if ref == nil {
		return "any", "", nil
	}
	if ref.Ref != "" && schemaManager != nil {
		if goType, imps, ok, err := schemaManager.ResolveRef(currentFile, "", ref.Ref); err != nil {
			return "", "", err
		} else if ok {
			for _, imp := range imps {
				imports[imp] = struct{}{}
			}
			return goType, "", nil
		}
	}
	if ref.Value == nil {
		return "any", "", nil
	}
	if customType, customImports := customGoType(ref.Value); customType != "" {
		for _, imp := range customImports {
			imports[imp] = struct{}{}
		}
		return customType, "", nil
	}
	if ref.Value.Type == nil {
		return "any", "", nil
	}
	if goType, typeImports, ok := gotypes.ResolvePrimitive(ref.Value); ok {
		for _, imp := range typeImports {
			imports[imp] = struct{}{}
		}
		return goType, "", nil
	}
	switch {
	case ref.Value.Type.Is("array"):
		itemType, itemDef, err := parseSchemaToType(currentFile, addGlobalType(typeName+"Item"), ref.Value.Items, schemaManager, imports)
		if err != nil {
			return "", "", err
		}
		return "[]" + itemType, itemDef, nil
	case ref.Value.Type.Is("object"):
		if len(ref.Value.Properties) == 0 {
			return "map[string]any", "", nil
		}
		return parseObjectSchema(currentFile, typeName, ref.Value, schemaManager, imports)
	default:
		return "any", "", nil
	}
}

func parseObjectSchema(currentFile, structName string, schema *openapi3.Schema, schemaManager *schema_codegen.Manager, imports map[string]struct{}) (string, string, error) {
	requiredFields := map[string]struct{}{}
	for _, r := range schema.Required {
		requiredFields[r] = struct{}{}
	}
	tmpl, err := template.New("struct").Parse(structTemplate)
	if err != nil {
		return "", "", err
	}
	propNames := make([]string, 0, len(schema.Properties))
	for name := range schema.Properties {
		propNames = append(propNames, name)
	}
	sort.Strings(propNames)
	fields := []Field{}
	var nestedDefs string
	for _, propName := range propNames {
		propRef := schema.Properties[propName]
		propType, propDef, err := parseSchemaToType(currentFile, addGlobalType(utils.UpperFirst(propName)), propRef, schemaManager, imports)
		if err != nil {
			return "", "", err
		}
		if propDef != "" {
			nestedDefs += propDef + "\n"
		}
		_, isRequired := requiredFields[propName]
		if !isRequired && !strings.HasPrefix(propType, "[]") && !strings.HasPrefix(propType, "map[") {
			propType = "*" + propType
		}
		description := ""
		if propRef != nil && propRef.Value != nil {
			description = propRef.Value.Description
		}
		fields = append(fields, Field{
			Name:        utils.UpperFirst(propName),
			Type:        propType,
			Description: descriptionToComment(description),
			Tag:         "`json:\"" + propName + "\" yaml:\"" + propName + "\"`",
		})
	}
	buf := bytes.NewBuffer(nil)
	if err := tmpl.Execute(buf, StructTemplateVars{StructName: structName, Fields: fields}); err != nil {
		return "", "", err
	}
	return structName, nestedDefs + "\n" + buf.String(), nil
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
		}
	}
	return goType, imports
}

func derefSchemaConfig(cfg *schema_codegen.Config) schema_codegen.Config {
	if cfg == nil {
		return schema_codegen.Config{}
	}
	return *cfg
}

func sortedImportSlice(imports map[string]struct{}) []string {
	ret := make([]string, 0, len(imports))
	for imp := range imports {
		ret = append(ret, imp)
	}
	sort.Strings(ret)
	return ret
}

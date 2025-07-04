package codegen

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/cloudcarver/anchor/pkg/utils"
	"gopkg.in/yaml.v3"
)

var globalTypeNameCounter = map[string]int{}

func resetGlobalTypeNameCounter() {
	globalTypeNameCounter = map[string]int{}
}

func process(data map[string]any, onFunc func(f Function) error, onParam func(name string, params map[string]any) error) error {
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
				return errors.New("timeout is not a valid duration, e.g. 1h, 1d, 1m")
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
			alwaysRetryOnFailure, ok := retryPolicyStr["always_retry_on_failure"].(bool)
			if !ok {
				return fmt.Errorf("always_retry_on_failure %v cannot be parsed to a boolean", retryPolicyStr["always_retry_on_failure"])
			}
			retryPolicy = &RetryPolicy{
				Interval:             interval,
				AlwaysRetryOnFailure: alwaysRetryOnFailure,
			}
		}

		// parse events
		var events *Events
		if _, ok := fnData["events"]; ok {
			eventsData, ok := fnData["events"].(map[string]any)
			if !ok {
				return errors.New("events cannot be parsed to a map")
			}
			events = &Events{}
			if onFailedData, ok := eventsData["onFailed"]; ok {
				onFailedStr, ok := onFailedData.(string)
				if !ok {
					return errors.New("events.onFailed cannot be parsed to a string")
				}
				events.OnFailed = &onFailedStr
			}
		}

		// parse parameters (optional)
		var structName string
		if _, ok := fnData["parameters"]; ok {
			parameters, ok := fnData["parameters"].(map[string]any)
			if !ok {
				return errors.New("parameters cannot be parsed to a map")
			}

			structName = addGlobalType(fmt.Sprintf("%sParameters", utils.UpperFirst(fnName)))
			if err := onParam(structName, parameters); err != nil {
				return err
			}
		} else {
			// For tasks without parameters, create a default parameter with taskID
			structName = addGlobalType(fmt.Sprintf("%sParameters", utils.UpperFirst(fnName)))
			defaultParams := map[string]any{
				"type": "object",
				"required": []any{"taskID"},
				"properties": map[string]any{
					"taskID": map[string]any{
						"type":        "integer",
						"format":      "int32",
						"description": "The ID of the task that triggered this event",
					},
				},
			}
			if err := onParam(structName, defaultParams); err != nil {
				return err
			}
		}

		if err := onFunc(Function{
			Name:          fnName,
			Description:   description,
			ParameterType: structName,
			Timeout:       timeout,
			Cronjob:       cronjob,
			RetryPolicy:   retryPolicy,
			Delay:         delay,
			Events:        events,
		}); err != nil {
			return err
		}
	}
	return nil
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

func Generate(workdir, packageName, taskDefPath, outPath string) error {
	raw, err := os.ReadFile(filepath.Join(workdir, taskDefPath))
	if err != nil {
		return err
	}
	var data map[string]any
	if err := yaml.Unmarshal(raw, &data); err != nil {
		return err
	}

	resetGlobalTypeNameCounter()

	result, err := generateToolInterfaces(packageName, data)
	if err != nil {
		return err
	}

	if err := os.WriteFile(filepath.Join(workdir, outPath), []byte(result), 0644); err != nil {
		return err
	}

	return nil
}

func generateToolInterfaces(packageName string, data map[string]any) (string, error) {
	var structDef string
	functions := []Function{}

	onFunc := func(f Function) error {
		functions = append(functions, f)
		return nil
	}

	onParam := func(name string, params map[string]any) error {
		def, err := parseObjectToStruct(name, params)
		if err != nil {
			return err
		}
		structDef += def + "\n"
		return nil
	}

	tcTemplate, err := template.New("file").Funcs(template.FuncMap{
		"upperFirst": utils.UpperFirst,
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

func parseArrayToStruct(name string, data map[string]any) (string, string, error) {
	items, ok := data["items"].(map[string]any)
	if !ok {
		return "", "", errors.New("items cannot be parsed to a map")
	}
	itemsType, ok := items["type"].(string)
	if !ok {
		return "", "", errors.New("items type cannot be parsed to a string")
	}
	itemsFormat, ok := items["format"].(string)
	if !ok {
		itemsFormat = ""
	}

	if itemsType == "object" {
		if _, ok := items["properties"]; !ok {
			return "[]any", "", nil
		}
		propStructName := utils.UpperFirst(name) + "Item"
		propStructDef, err := parseObjectToStruct(propStructName, items)
		if err != nil {
			return "", "", err
		}
		return "[]" + propStructName, propStructDef, nil
	} else if itemsType == "array" {
		propStructName, propStructDef, err := parseArrayToStruct(name, items)
		if err != nil {
			return "", "", err
		}
		return "[]" + propStructName, propStructDef, nil
	} else {
		return "[]" + typeMap(itemsType, itemsFormat), "", nil
	}
}

func typeMap(typeName string, format string) string {
	switch typeName {
	case "string":
		return "string"
	case "integer":
		if format == "int64" {
			return "int64"
		} else if format == "int32" {
			return "int32"
		}
		return "int"
	case "number":
		return "float64"
	case "boolean":
		return "bool"
	default:
		return typeName
	}
}

// return struct name, struct definition, error
func parseObjectToStruct(structName string, object map[string]any) (string, error) {
	var ok bool
	var requiredFields = map[string]struct{}{}
	var properties map[string]any
	var structDef string

	if _, ok := object["properties"]; !ok {
		return "", nil
	}

	properties, ok = object["properties"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("properties %v cannot be parsed to map[string]map[string]any", object["properties"])
	}

	if _, ok := object["required"]; ok {
		required, ok := object["required"].([]any)
		if !ok {
			return "", fmt.Errorf("required %v cannot be parsed to a string array", object["required"])
		}
		for _, r := range required {
			if _, ok := properties[r.(string)]; !ok {
				return "", fmt.Errorf("required field %s is not in properties", r)
			}
			requiredFields[r.(string)] = struct{}{}
		}
	}

	tmpl, err := template.New("struct").Parse(structTemplate)
	if err != nil {
		return "", err
	}

	fields := []Field{}

	for propName, propRaw := range properties {
		prop, ok := propRaw.(map[string]any)
		if !ok {
			return "", fmt.Errorf("property %s cannot be parsed to a map", propName)
		}
		propType, ok := prop["type"].(string)
		if !ok {
			return "", errors.New("property type cannot be parsed to a string")
		}
		propFormat, ok := prop["format"].(string)
		if !ok {
			propFormat = ""
		}

		var propDescription string
		if _, ok := prop["description"]; ok {
			propDescription, ok = prop["description"].(string)
			if !ok {
				return "", errors.New("property description cannot be parsed to a string")
			}
		}

		_, isRequired := requiredFields[propName]

		if propType == "object" {
			if _, ok := prop["properties"]; !ok {
				propType = "any"
			} else {
				propStructName := addGlobalType(utils.UpperFirst(propName))
				propStructDef, err := parseObjectToStruct(propStructName, prop)
				if err != nil {
					return "", err
				}
				propType = propStructName
				structDef += propStructDef + "\n"
			}
		} else if propType == "array" {
			propStructName, propStructDef, err := parseArrayToStruct(propName, prop)
			if err != nil {
				return "", err
			}
			propType = propStructName
			structDef += propStructDef + "\n"
		} else {
			propType = typeMap(propType, propFormat)
		}

		fields = append(fields, Field{
			Name:        utils.UpperFirst(propName),
			Type:        utils.IfElse(isRequired || strings.HasPrefix(propType, "[]"), "", "*") + propType,
			Description: descriptionToComment(propDescription),
			Tag:         "`json:\"" + propName + "\" yaml:\"" + propName + "\"`",
		})
	}

	templateVars := StructTemplateVars{
		StructName: structName,
		Fields:     fields,
	}
	buf := bytes.NewBuffer([]byte{})

	if err := tmpl.Execute(buf, templateVars); err != nil {
		return "", err
	}

	return structDef + "\n" + buf.String(), nil
}

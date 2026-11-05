package jsondecode

import (
	"fmt"
	"go/ast"
	"go/parser"
	"strings"
)

func (e *emitter) scalarDecoder(out *strings.Builder, node *rule, goType string) error {
	t := node.Target
	if t.Converter != "" {
		expr, err := parser.ParseExpr(t.Converter)
		if err != nil {
			return fmt.Errorf("invalid x-go-converter: %w", err)
		}
		switch expr.(type) {
		case *ast.Ident, *ast.SelectorExpr:
		default:
			return fmt.Errorf("x-go-converter must name a function")
		}
	}
	if t.Bits == "strconv.IntSize" {
		e.use("strconv")
	}
	stringCheck, err := e.predicate(node.Schema, "string", "text")
	if err != nil {
		return err
	}
	out.WriteString("switch data[0] {\ncase '\"':\n")
	if stringCheck == "false" {
		out.WriteString("return value, fmt.Errorf(\"string does not match input schema\")\n")
	} else {
		if stringCheck != "true" || t.Converter == "" && t.Kind != "custom" {
			e.use(runtimeImport)
			out.WriteString("text, textErr := jsondecode.UnquoteString(data)\nif textErr != nil { return value, textErr }\n")
		}
		check(out, stringCheck, "string")
		if t.Converter != "" || t.Kind == "custom" {
			e.customConversion(out, t)
		} else {
			switch t.Kind {
			case "signed", "unsigned":
				e.use("strconv")
				parser := "ParseInt"
				if t.Kind == "unsigned" {
					parser = "ParseUint"
				}
				fmt.Fprintf(out, "converted, err := strconv.%s(text, 10, %s)\nif err != nil { return value, err }\nreturn %s(converted), nil\n", parser, t.Bits, goType)
			case "float":
				e.use(runtimeImport)
				fmt.Fprintf(out, "converted, err := jsondecode.ParseFloatString(text, %s)\nif err != nil { return value, err }\nreturn %s(converted), nil\n", t.Bits, goType)
			case "bool":
				fmt.Fprintf(out, "switch text { case \"true\": return %s(true), nil; case \"false\": return %s(false), nil; default: return value, fmt.Errorf(\"expected true or false\") }\n", goType, goType)
			case "string":
				fmt.Fprintf(out, "return %s(text), nil\n", goType)
			}
		}
	}
	out.WriteString("case 't', 'f':\n")
	boolCheck := "false"
	if t.Kind == "bool" || t.Kind == "string" || t.Kind == "custom" || t.Converter != "" {
		boolCheck, err = e.predicate(node.Schema, "boolean", "boolean")
		if err != nil {
			return err
		}
	}
	if boolCheck == "false" {
		out.WriteString("return value, fmt.Errorf(\"boolean input is not allowed\")\n")
	} else {
		if strings.Contains(boolCheck, "boolean") || t.Kind == "bool" && t.Converter == "" {
			out.WriteString("boolean := data[0] == 't'\n")
		}
		check(out, boolCheck, "boolean")
		if t.Kind == "custom" || t.Converter != "" {
			e.customConversion(out, t)
		} else if t.Kind == "bool" {
			fmt.Fprintf(out, "return %s(boolean), nil\n", goType)
		} else {
			fmt.Fprintf(out, "return %s(data), nil\n", goType)
		}
	}
	out.WriteString("default:\nif data[0] != '-' && (data[0] < '0' || data[0] > '9') { return value, fmt.Errorf(\"expected a scalar input\") }\n")
	if t.Kind == "bool" && t.Converter == "" {
		out.WriteString("return value, fmt.Errorf(\"numeric input cannot be converted to bool\")\n}\n")
		return nil
	}
	kind := "number"
	if (t.Kind == "signed" || t.Kind == "unsigned") && t.Converter == "" {
		kind = t.Kind
	}
	numberCheck, err := e.predicate(node.Schema, kind, "number")
	if err != nil {
		return err
	}
	if numberCheck == "false" {
		out.WriteString("return value, fmt.Errorf(\"number does not match input schema\")\n}\n")
		return nil
	}
	if kind == "signed" || kind == "unsigned" {
		e.use(runtimeImport)
		parser := "ParseIntNumber"
		if kind == "unsigned" {
			parser = "ParseUintNumber"
		}
		fmt.Fprintf(out, "number, err := jsondecode.%s(string(data), %s)\nif err != nil { return value, err }\n", parser, t.Bits)
	} else if strings.Contains(numberCheck, "number.") {
		e.use(runtimeImport)
		out.WriteString("number := jsondecode.NumberFromJSON(string(data))\n")
	}
	check(out, numberCheck, "number")
	switch {
	case t.Kind == "custom" || t.Converter != "":
		e.customConversion(out, t)
	case kind == "signed" || kind == "unsigned":
		fmt.Fprintf(out, "return %s(number), nil\n", goType)
	case t.Kind == "string":
		fmt.Fprintf(out, "return %s(data), nil\n", goType)
	case t.Kind == "float":
		e.use("strconv")
		fmt.Fprintf(out, "converted, err := strconv.ParseFloat(string(data), %s)\nif err != nil { return value, err }\nreturn %s(converted), nil\n", t.Bits, goType)
	}
	out.WriteString("}\n")
	return nil
}

func (e *emitter) customConversion(out *strings.Builder, t *target) {
	if t.Converter != "" {
		fmt.Fprintf(out, "return %s(json.RawMessage(data))\n", t.Converter)
	} else {
		out.WriteString("err := json.Unmarshal(data, &value)\nreturn value, err\n")
	}
}

func check(out *strings.Builder, condition, kind string) {
	if condition != "true" {
		fmt.Fprintf(out, "if !(%s) { return value, fmt.Errorf(%q) }\n", condition, kind+" does not match input schema")
	}
}

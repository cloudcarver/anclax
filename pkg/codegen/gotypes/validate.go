package gotypes

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"strconv"
	"strings"
)

func ValidateIdentifier(name string) error {
	if name == "_" || !token.IsIdentifier(name) {
		return fmt.Errorf("invalid Go identifier %q", name)
	}
	return nil
}

func ValidateTypeExpression(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("Go type expression is empty")
	}
	if containsComment(value) {
		return fmt.Errorf("Go type expression must not contain comments")
	}
	expr, err := parser.ParseExpr(value)
	if err != nil {
		return fmt.Errorf("invalid Go type expression %q: %w", value, err)
	}
	if err := validateTypeNode(expr); err != nil {
		return fmt.Errorf("invalid Go type expression %q: %w", value, err)
	}
	return nil
}

func EnumLiteral(goType string, value any) (string, error) {
	if goType == "string" {
		return strconv.Quote(fmt.Sprint(value)), nil
	}
	literal := fmt.Sprint(value)
	expr, err := parser.ParseExpr(literal)
	if err != nil {
		return "", fmt.Errorf("invalid %s enum literal %q: %w", goType, literal, err)
	}
	switch {
	case goType == "bool":
		ident, ok := expr.(*ast.Ident)
		if !ok || (ident.Name != "true" && ident.Name != "false") {
			return "", fmt.Errorf("invalid bool enum literal %q", literal)
		}
	case strings.HasPrefix(goType, "int"), strings.HasPrefix(goType, "uint"):
		if !isNumericLiteral(expr, false) {
			return "", fmt.Errorf("invalid integer enum literal %q", literal)
		}
	case strings.HasPrefix(goType, "float"):
		if !isNumericLiteral(expr, true) {
			return "", fmt.Errorf("invalid number enum literal %q", literal)
		}
	default:
		return "", fmt.Errorf("unsupported enum base type %q", goType)
	}
	return literal, nil
}

func isNumericLiteral(expr ast.Expr, allowFloat bool) bool {
	if unary, ok := expr.(*ast.UnaryExpr); ok {
		if unary.Op != token.ADD && unary.Op != token.SUB {
			return false
		}
		expr = unary.X
	}
	literal, ok := expr.(*ast.BasicLit)
	if !ok {
		return false
	}
	return literal.Kind == token.INT || (allowFloat && literal.Kind == token.FLOAT)
}

func containsComment(value string) bool {
	fset := token.NewFileSet()
	file := fset.AddFile("type", -1, len(value))
	var s scanner.Scanner
	s.Init(file, []byte(value), nil, scanner.ScanComments)
	for {
		_, tok, _ := s.Scan()
		if tok == token.COMMENT {
			return true
		}
		if tok == token.EOF {
			return false
		}
	}
}

func validateTypeNode(expr ast.Expr) error {
	switch typed := expr.(type) {
	case *ast.Ident:
		return ValidateIdentifier(typed.Name)
	case *ast.SelectorExpr:
		if err := validateSelectorBase(typed.X); err != nil {
			return err
		}
		return ValidateIdentifier(typed.Sel.Name)
	case *ast.StarExpr:
		return validateTypeNode(typed.X)
	case *ast.ArrayType:
		if typed.Len != nil {
			literal, ok := typed.Len.(*ast.BasicLit)
			if !ok || literal.Kind != token.INT {
				return fmt.Errorf("array length must be an integer literal")
			}
			if _, err := strconv.ParseUint(literal.Value, 0, 64); err != nil {
				return fmt.Errorf("invalid array length: %w", err)
			}
		}
		return validateTypeNode(typed.Elt)
	case *ast.MapType:
		if err := validateTypeNode(typed.Key); err != nil {
			return err
		}
		return validateTypeNode(typed.Value)
	case *ast.IndexExpr:
		if err := validateTypeNode(typed.X); err != nil {
			return err
		}
		return validateTypeNode(typed.Index)
	case *ast.IndexListExpr:
		if err := validateTypeNode(typed.X); err != nil {
			return err
		}
		for _, index := range typed.Indices {
			if err := validateTypeNode(index); err != nil {
				return err
			}
		}
		return nil
	case *ast.ParenExpr:
		return validateTypeNode(typed.X)
	default:
		return fmt.Errorf("unsupported type construct %T", expr)
	}
}

func validateSelectorBase(expr ast.Expr) error {
	switch typed := expr.(type) {
	case *ast.Ident:
		return ValidateIdentifier(typed.Name)
	case *ast.SelectorExpr:
		return validateTypeNode(typed)
	default:
		return fmt.Errorf("invalid selector base %T", expr)
	}
}

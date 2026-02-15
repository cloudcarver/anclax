package dst

import (
	"strings"

	"github.com/pkg/errors"
)

func ParseMethodSignature(raw string) (ParsedMethod, error) {
	sig := strings.TrimSpace(raw)
	if sig == "" {
		return ParsedMethod{}, errors.New("empty method signature")
	}

	open := strings.Index(sig, "(")
	close := strings.LastIndex(sig, ")")
	if open <= 0 || close < open {
		return ParsedMethod{}, errors.Errorf("invalid method signature %q", raw)
	}

	name := strings.TrimSpace(sig[:open])
	if name == "" {
		return ParsedMethod{}, errors.Errorf("invalid method name in signature %q", raw)
	}

	paramsPart := strings.TrimSpace(sig[open+1 : close])
	returnsPart := strings.TrimSpace(sig[close+1:])
	if returnsPart == "" {
		returnsPart = "error"
	}
	if returnsPart != "error" {
		return ParsedMethod{}, errors.Errorf("only error return is supported for now, got %q in %q", returnsPart, raw)
	}

	params, err := parseMethodParams(paramsPart)
	if err != nil {
		return ParsedMethod{}, errors.Wrapf(err, "invalid params in signature %q", raw)
	}

	return ParsedMethod{
		Raw:     raw,
		Name:    name,
		Params:  params,
		Returns: returnsPart,
	}, nil
}

func parseMethodParams(raw string) ([]ParsedParam, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := splitComma(raw)
	params := make([]ParsedParam, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		fields := strings.Fields(p)
		if len(fields) < 2 {
			return nil, errors.Errorf("param %q should be in form '<name> <type>'", p)
		}
		name := fields[0]
		typ := strings.Join(fields[1:], " ")
		params = append(params, ParsedParam{Name: name, Type: typ})
	}
	return params, nil
}

func ParseCallExpression(raw string) (ParsedCall, error) {
	expr := strings.TrimSpace(raw)
	if expr == "" {
		return ParsedCall{}, errors.New("empty call expression")
	}
	open := strings.Index(expr, "(")
	close := strings.LastIndex(expr, ")")
	if open <= 0 || close < open {
		return ParsedCall{}, errors.Errorf("invalid call expression %q", raw)
	}
	method := strings.TrimSpace(expr[:open])
	if method == "" {
		return ParsedCall{}, errors.Errorf("invalid call expression %q", raw)
	}
	argsRaw := strings.TrimSpace(expr[open+1 : close])
	args := make([]string, 0)
	if argsRaw != "" {
		for _, a := range splitComma(argsRaw) {
			a = strings.TrimSpace(a)
			if a == "" {
				continue
			}
			args = append(args, a)
		}
	}
	return ParsedCall{Raw: raw, Method: method, Args: args}, nil
}

func splitComma(raw string) []string {
	// Minimal splitter (v1): split by commas at top-level with simple bracket depth.
	out := []string{}
	start := 0
	depthParen := 0
	depthBracket := 0
	depthBrace := 0
	for i, ch := range raw {
		switch ch {
		case '(':
			depthParen++
		case ')':
			if depthParen > 0 {
				depthParen--
			}
		case '[':
			depthBracket++
		case ']':
			if depthBracket > 0 {
				depthBracket--
			}
		case '{':
			depthBrace++
		case '}':
			if depthBrace > 0 {
				depthBrace--
			}
		case ',':
			if depthParen == 0 && depthBracket == 0 && depthBrace == 0 {
				out = append(out, raw[start:i])
				start = i + 1
			}
		}
	}
	out = append(out, raw[start:])
	return out
}

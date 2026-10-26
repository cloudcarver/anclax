package codegen

import (
	_ "embed"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/cloudcarver/anclax"
)

// The generated clients include these runtime templates and the canonical SQL.
// No Python/Node.js package or source checkout is needed when using the output.
//
//go:embed client_templates/python.py.tmpl
var pythonClientRuntime string

//go:embed client_templates/nodejs.js.tmpl
var nodeClientRuntime string

//go:embed client_templates/nodejs.d.ts.tmpl
var nodeClientDeclarations string

var clientQueryNames = []string{
	"CreateTask",
	"GetTaskByUniqueTag",
	"GetTaskByID",
	"GetLastTaskErrorEvent",
	"UpdatePendingTaskPriorityByLabels",
	"UpdatePendingTaskWeightByLabels",
}

var clientQueryHeader = regexp.MustCompile(`(?m)^-- name: (\w+) :\w+\r?\n`)
var clientQueryArgument = regexp.MustCompile(`sqlc\.arg\((\w+)\)`)
var clientQueryPosition = regexp.MustCompile(`\$\d+`)

func clientQueries() (map[string]string, error) {
	raw, err := anclax.Migrations.ReadFile("sql/queries/tasks.sql")
	if err != nil {
		return nil, err
	}
	return extractClientQueries(string(raw))
}

func extractClientQueries(source string) (map[string]string, error) {
	wanted := make(map[string]bool, len(clientQueryNames))
	for _, name := range clientQueryNames {
		wanted[name] = true
	}
	queries := make(map[string]string, len(wanted))
	headers := clientQueryHeader.FindAllStringSubmatchIndex(source, -1)
	for i, header := range headers {
		name := source[header[2]:header[3]]
		if !wanted[name] {
			continue
		}
		end := len(source)
		if i+1 < len(headers) {
			end = headers[i+1][0]
		}
		query := strings.TrimSpace(source[header[1]:end])
		if clientQueryArgument.MatchString(query) && clientQueryPosition.MatchString(query) {
			return nil, fmt.Errorf("task client query %s mixes named and positional parameters", name)
		}
		arguments := map[string]int{}
		queries[name] = clientQueryArgument.ReplaceAllStringFunc(query, func(arg string) string {
			if _, ok := arguments[arg]; !ok {
				arguments[arg] = len(arguments) + 1
			}
			return "$" + strconv.Itoa(arguments[arg])
		})
	}
	for _, name := range clientQueryNames {
		if queries[name] == "" {
			return nil, fmt.Errorf("task client query %s is missing from sql/queries/tasks.sql", name)
		}
	}
	return queries, nil
}

func renderClientQueries(language string, queries map[string]string) string {
	if language == "python" {
		var code strings.Builder
		code.WriteString("class _SQL:\n")
		for _, name := range clientQueryNames {
			fmt.Fprintf(&code, "    %s = %s\n", name, clientLiteral(queries[name], language))
		}
		return code.String()
	}
	return "const sql = " + clientLiteral(queries, language) + ";\n"
}

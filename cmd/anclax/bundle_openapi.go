package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	openapi_bundle "github.com/cloudcarver/anclax/pkg/openapi/bundle"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
	"gopkg.in/yaml.v3"
)

var bundleOpenAPISpecCmd = &cli.Command{
	Name:  "bundle-openapi-spec",
	Usage: "Bundle an OpenAPI file or directory into a single OpenAPI document",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:     "input",
			Aliases:  []string{"i"},
			Usage:    "Path to the OpenAPI source file or directory",
			Required: true,
		},
		&cli.StringFlag{
			Name:     "output",
			Aliases:  []string{"o"},
			Usage:    "Path to the bundled OpenAPI output file",
			Required: true,
		},
	},
	Action: runBundleOpenAPISpec,
}

func runBundleOpenAPISpec(c *cli.Context) error {
	workdir := c.Args().First()
	if workdir == "" {
		workdir = "."
	}
	return bundleOpenAPISpec(workdir, c.String("input"), c.String("output"))
}

func bundleOpenAPISpec(workdir, inputPath, outputPath string) (retErr error) {
	resolvedInputPath, err := resolveCLIPath(workdir, inputPath)
	if err != nil {
		return err
	}
	resolvedOutputPath, err := resolveCLIPath(workdir, outputPath)
	if err != nil {
		return err
	}
	if info, err := os.Stat(resolvedInputPath); err == nil && info.IsDir() && isWithinDir(resolvedOutputPath, resolvedInputPath) {
		return errors.Errorf("bundle output %s must be outside OpenAPI input directory %s", outputPath, inputPath)
	}

	doc, _, err := openapi_bundle.Load(workdir, inputPath)
	if err != nil {
		return errors.Wrap(err, "failed to load OpenAPI spec")
	}
	if err := doc.Validate(context.Background()); err != nil {
		return errors.Wrap(err, "failed to validate OpenAPI spec before bundling")
	}

	resolver := strictRefNameResolver()
	defer func() {
		if recovered := recover(); recovered != nil {
			switch typed := recovered.(type) {
			case error:
				retErr = typed
			default:
				retErr = errors.Errorf("failed to internalize OpenAPI refs: %v", recovered)
			}
		}
	}()
	doc.InternalizeRefs(context.Background(), resolver)

	if err := doc.Validate(context.Background()); err != nil {
		return errors.Wrap(err, "failed to validate bundled OpenAPI spec")
	}
	buf, err := marshalOrderedOpenAPIYAMLWithIndent(doc, 2)
	if err != nil {
		return errors.Wrap(err, "failed to marshal bundled OpenAPI spec")
	}
	buf = append([]byte(generatedBundleHeader(inputPath)), buf...)

	if err := os.MkdirAll(filepath.Dir(resolvedOutputPath), 0755); err != nil {
		return errors.Wrap(err, "failed to create output directory")
	}
	if err := os.WriteFile(resolvedOutputPath, buf, 0644); err != nil {
		return errors.Wrap(err, "failed to write bundled OpenAPI spec")
	}
	return nil
}

func strictRefNameResolver() func(*openapi3.T, openapi3.ComponentRef) string {
	owners := map[string]string{}
	return func(doc *openapi3.T, ref openapi3.ComponentRef) string {
		name := refComponentName(ref)
		if name == "" {
			name = openapi3.DefaultRefNameResolver(doc, ref)
		}
		ownerKey := ref.CollectionName() + ":" + name
		refString := ref.RefString()
		if previous, ok := owners[ownerKey]; ok && previous != refString {
			panic(errors.Errorf("duplicate bundled component %s.%s from %s and %s", ref.CollectionName(), name, previous, refString))
		}
		owners[ownerKey] = refString
		return name
	}
}

func refComponentName(ref openapi3.ComponentRef) string {
	refPath := ref.RefPath()
	if refPath == nil {
		return ""
	}
	fragment := strings.TrimPrefix(refPath.Fragment, "/")
	if fragment == "" {
		return ""
	}
	parts := strings.Split(fragment, "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

func resolveCLIPath(workdir, path string) (string, error) {
	if path == "" {
		return "", errors.New("path is required")
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	if workdir == "" {
		workdir = "."
	}
	return filepath.Clean(filepath.Join(workdir, path)), nil
}

func isWithinDir(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, "..") && rel != "")
}

func marshalOrderedOpenAPIYAMLWithIndent(v any, indent int) ([]byte, error) {
	raw, err := marshalYAMLWithIndent(v, indent)
	if err != nil {
		return nil, err
	}

	var node yaml.Node
	if err := yaml.Unmarshal(raw, &node); err != nil {
		return nil, err
	}
	reorderOpenAPITopLevelKeys(&node)
	return marshalYAMLWithIndent(&node, indent)
}

func marshalYAMLWithIndent(v any, indent int) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(indent)
	if err := enc.Encode(v); err != nil {
		_ = enc.Close()
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func reorderOpenAPITopLevelKeys(root *yaml.Node) {
	if root == nil || len(root.Content) == 0 {
		return
	}
	mapping := root.Content[0]
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return
	}

	type pair struct {
		key  string
		keyN *yaml.Node
		valN *yaml.Node
	}

	fixedOrder := []string{"openapi", "info", "servers", "paths", "components"}
	fixedMap := make(map[string]pair, len(fixedOrder))
	var xPairs []pair
	var otherPairs []pair

	for i := 0; i+1 < len(mapping.Content); i += 2 {
		p := pair{key: mapping.Content[i].Value, keyN: mapping.Content[i], valN: mapping.Content[i+1]}
		switch {
		case containsString(fixedOrder, p.key):
			fixedMap[p.key] = p
		case strings.HasPrefix(p.key, "x-"):
			xPairs = append(xPairs, p)
		default:
			otherPairs = append(otherPairs, p)
		}
	}

	sort.Slice(xPairs, func(i, j int) bool { return xPairs[i].key < xPairs[j].key })
	sort.Slice(otherPairs, func(i, j int) bool { return otherPairs[i].key < otherPairs[j].key })

	reordered := make([]*yaml.Node, 0, len(mapping.Content))
	for _, key := range fixedOrder {
		if p, ok := fixedMap[key]; ok {
			reordered = append(reordered, p.keyN, p.valN)
		}
	}
	for _, p := range xPairs {
		reordered = append(reordered, p.keyN, p.valN)
	}
	for _, p := range otherPairs {
		reordered = append(reordered, p.keyN, p.valN)
	}
	mapping.Content = reordered
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func generatedBundleHeader(inputPath string) string {
	return "# DO NOT EDIT. Generated Code. The single source of truth is " + filepath.ToSlash(inputPath) + "\n"
}

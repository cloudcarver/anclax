package main

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/cloudcarver/anclax/pkg/codegen/codegenpath"
	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"
)

func validateCodegenPaths(workdir string, config *Config) error {
	validate := func(label, path string) error {
		if path == "" {
			return nil
		}
		if _, err := codegenpath.Resolve(workdir, path); err != nil {
			return errors.Wrapf(err, "invalid %s", label)
		}
		return nil
	}

	if config.Schemas != nil {
		if config.Schemas.Output != "" {
			if _, err := codegenpath.ResolveSubpath(workdir, config.Schemas.Output); err != nil {
				return errors.Wrap(err, "invalid schemas.output")
			}
		}
	}
	for i, item := range config.OapiCodegen {
		if err := validate(fmt.Sprintf("oapi-codegen[%d].out", i), item.Out); err != nil {
			return err
		}
	}
	for i, item := range config.TaskHandler {
		if err := validate(fmt.Sprintf("task-handler[%d].out", i), item.Out); err != nil {
			return err
		}
	}
	for i, item := range config.DST {
		if err := validate(fmt.Sprintf("dst[%d].out", i), item.Out); err != nil {
			return err
		}
	}
	for i, pattern := range config.CleanItems {
		if _, err := codegenpath.ResolvePattern(workdir, pattern); err != nil {
			return errors.Wrapf(err, "invalid clean[%d]", i)
		}
	}
	if err := validate("anclaxdef", config.AnclaxDef); err != nil {
		return err
	}
	if config.Mockgen != nil {
		for i, file := range config.Mockgen.Files {
			if err := validate(fmt.Sprintf("mockgen.files[%d].destination", i), file.Destination); err != nil {
				return err
			}
		}
	}
	for i, item := range config.Wire {
		if item.Path != "" {
			if _, err := resolveWireDir(workdir, item.Path); err != nil {
				return errors.Wrapf(err, "invalid wire[%d].path", i)
			}
		}
	}
	for i, item := range config.Sqlc {
		if err := validate(fmt.Sprintf("sqlc[%d].path", i), item.Path); err != nil {
			return err
		}
		if item.Path != "" {
			if err := validateSQLCOutputs(workdir, item.Path); err != nil {
				return errors.Wrapf(err, "invalid sqlc[%d] outputs", i)
			}
		}
	}
	return nil
}

func resolveWireDir(workdir, path string) (string, error) {
	resolved, err := codegenpath.Resolve(workdir, path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", errors.Wrap(err, "stat wire directory")
	}
	if !info.IsDir() {
		return "", errors.Errorf("wire path %q is not a directory", path)
	}
	if _, err := codegenpath.Resolve(workdir, filepath.Join(path, "wire_gen.go")); err != nil {
		return "", errors.Wrap(err, "invalid wire output")
	}
	return resolved, nil
}

func validateSQLCOutputs(workdir, configPath string) error {
	resolvedConfig, err := codegenpath.Resolve(workdir, configPath)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(resolvedConfig)
	if err != nil {
		return errors.Wrap(err, "read sqlc config")
	}
	var document yaml.Node
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return errors.Wrap(err, "parse sqlc config")
	}
	configDir, err := codegenpath.Rel(workdir, filepath.Dir(resolvedConfig))
	if err != nil {
		return errors.Wrap(err, "resolve sqlc config directory")
	}
	return validateSQLCConfig(&document, func(output string, custom bool) error {
		if output == "" {
			return nil
		}
		resolvedOutput := output
		if !filepath.IsAbs(output) && filepath.VolumeName(output) == "" {
			resolvedOutput = filepath.Join(configDir, output)
		}
		if _, err := codegenpath.ResolveSubpath(workdir, resolvedOutput); err != nil {
			return errors.Wrapf(err, "sqlc output %q", output)
		}
		if err := codegenpath.ValidateTree(workdir, resolvedOutput); err != nil {
			return errors.Wrapf(err, "sqlc output tree %q", output)
		}
		if custom {
			if err := validateSQLCCustomOutputSiblings(workdir, resolvedOutput); err != nil {
				return errors.Wrapf(err, "sqlc custom output %q", output)
			}
		}
		return nil
	})
}

func validateSQLCCustomOutputSiblings(workdir, output string) error {
	parent, err := codegenpath.Resolve(workdir, filepath.Dir(output))
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(parent)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return errors.Wrap(err, "read custom output parent")
	}
	parentRel, err := codegenpath.Rel(workdir, parent)
	if err != nil {
		return err
	}
	base := filepath.Base(filepath.Clean(output))
	for _, entry := range entries {
		if !strings.HasPrefix(strings.ToLower(entry.Name()), strings.ToLower(base)) {
			continue
		}
		candidate := filepath.Join(parentRel, entry.Name())
		if err := codegenpath.ValidateTree(workdir, candidate); err != nil {
			return errors.Wrapf(err, "unsafe output-prefix sibling %q", candidate)
		}
	}
	return nil
}

func validateSQLCConfig(document *yaml.Node, visitOutput func(string, bool) error) error {
	root := derefYAMLNode(document)
	if root != nil && root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		root = derefYAMLNode(root.Content[0])
	}
	if root == nil || root.Kind != yaml.MappingNode {
		return errors.New("sqlc config must be a mapping")
	}
	versionNode, ok := yamlMappingValue(root, "version")
	if !ok {
		return errors.New("sqlc config version is required")
	}
	version, ok := sqlcOutputValue(versionNode)
	if !ok {
		return errors.New("sqlc config version must be a scalar")
	}
	switch version {
	case "1":
		return validateSQLCV1(root, visitOutput)
	case "2":
		return validateSQLCV2(root, visitOutput)
	default:
		return errors.Errorf("unsupported sqlc config version %q", version)
	}
}

func validateSQLCV1(root *yaml.Node, visitOutput func(string, bool) error) error {
	packagesNode, ok := yamlMappingValue(root, "packages")
	if !ok {
		return nil
	}
	for _, packageNode := range yamlSequence(packagesNode) {
		packageNode = derefYAMLNode(packageNode)
		if packageNode == nil || packageNode.Kind != yaml.MappingNode {
			continue
		}
		if err := visitSQLCMappingPath(packageNode, "path", false, visitOutput); err != nil {
			return err
		}
		if err := validateSQLCFileOptions(packageNode); err != nil {
			return err
		}
	}
	return nil
}

func validateSQLCV2(root *yaml.Node, visitOutput func(string, bool) error) error {
	sqlNode, ok := yamlMappingValue(root, "sql")
	if !ok {
		return nil
	}
	for _, querySet := range yamlSequence(sqlNode) {
		querySet = derefYAMLNode(querySet)
		if querySet == nil || querySet.Kind != yaml.MappingNode {
			continue
		}
		if codegen, ok := yamlMappingValue(querySet, "codegen"); ok {
			for _, customGenerator := range yamlSequence(codegen) {
				customGenerator = derefYAMLNode(customGenerator)
				if customGenerator == nil || customGenerator.Kind != yaml.MappingNode {
					continue
				}
				if err := visitSQLCMappingPath(customGenerator, "out", true, visitOutput); err != nil {
					return err
				}
			}
		}
		gen, ok := yamlMappingValue(querySet, "gen")
		if !ok {
			continue
		}
		gen = derefYAMLNode(gen)
		for _, generator := range yamlMappingKeys(gen) {
			if generator == "go" || generator == "json" {
				continue
			}
			if options, ok := yamlMappingValue(gen, generator); ok && yamlNodeNonEmpty(options) {
				return errors.Errorf("unsupported sqlc generator %q cannot be checked for output paths", generator)
			}
		}
		for _, generator := range []string{"go", "json"} {
			options, ok := yamlMappingValue(gen, generator)
			if !ok || !yamlNodeNonEmpty(options) {
				continue
			}
			options = derefYAMLNode(options)
			if err := visitSQLCMappingPath(options, "out", false, visitOutput); err != nil {
				return err
			}
			if err := validateSQLCFileOptions(options); err != nil {
				return err
			}
		}
	}
	return nil
}

func visitSQLCMappingPath(node *yaml.Node, key string, custom bool, visit func(string, bool) error) error {
	valueNode, ok := yamlMappingValue(node, key)
	if !ok {
		return nil
	}
	value, ok := sqlcOutputValue(valueNode)
	if !ok || value == "" {
		return nil
	}
	return visit(value, custom)
}

func validateSQLCFileOptions(node *yaml.Node) error {
	for _, key := range []string{
		"filename",
		"output_batch_file_name",
		"output_db_file_name",
		"output_models_file_name",
		"output_querier_file_name",
		"output_copyfrom_file_name",
		"output_files_suffix",
	} {
		valueNode, ok := yamlMappingValue(node, key)
		if !ok {
			continue
		}
		value, ok := sqlcOutputValue(valueNode)
		if ok {
			if err := validateSQLCFileOption(key, value); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateSQLCFileOption(key, value string) error {
	if value == "" {
		return nil
	}
	if strings.ContainsRune(value, 0) || filepath.IsAbs(value) || filepath.VolumeName(value) != "" {
		return errors.Errorf("sqlc %s %q must be a safe relative filename", key, value)
	}
	if key == "output_files_suffix" {
		if strings.ContainsAny(value, `/\`) {
			return errors.Errorf("sqlc %s %q must be a single safe filename suffix", key, value)
		}
		return nil
	}
	normalized := strings.ReplaceAll(value, `\`, "/")
	cleaned := path.Clean(normalized)
	if strings.HasPrefix(normalized, "/") || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return errors.Errorf("sqlc %s %q escapes its configured output directory", key, value)
	}
	return nil
}

func yamlMappingValue(node *yaml.Node, key string) (*yaml.Node, bool) {
	return yamlMappingValueSeen(node, key, map[*yaml.Node]bool{})
}

func yamlMappingValueSeen(node *yaml.Node, key string, seen map[*yaml.Node]bool) (*yaml.Node, bool) {
	node = derefYAMLNode(node)
	if node == nil || node.Kind != yaml.MappingNode || seen[node] {
		return nil, false
	}
	seen[node] = true
	for i := 0; i+1 < len(node.Content); i += 2 {
		keyName, ok := sqlcOutputValue(node.Content[i])
		if ok && keyName == key && keyName != "<<" {
			return node.Content[i+1], true
		}
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		keyName, ok := sqlcOutputValue(node.Content[i])
		if !ok || keyName != "<<" {
			continue
		}
		for _, merged := range yamlMergedMappings(node.Content[i+1]) {
			if value, ok := yamlMappingValueSeen(merged, key, seen); ok {
				return value, true
			}
		}
	}
	return nil, false
}

func yamlMergedMappings(node *yaml.Node) []*yaml.Node {
	node = derefYAMLNode(node)
	if node == nil {
		return nil
	}
	if node.Kind == yaml.MappingNode {
		return []*yaml.Node{node}
	}
	if node.Kind != yaml.SequenceNode {
		return nil
	}
	var mappings []*yaml.Node
	for _, child := range node.Content {
		child = derefYAMLNode(child)
		if child != nil && child.Kind == yaml.MappingNode {
			mappings = append(mappings, child)
		}
	}
	return mappings
}

func yamlSequence(node *yaml.Node) []*yaml.Node {
	node = derefYAMLNode(node)
	if node == nil {
		return nil
	}
	if node.Kind == yaml.SequenceNode {
		return node.Content
	}
	return nil
}

func yamlMappingKeys(node *yaml.Node) []string {
	keys := map[string]struct{}{}
	var collect func(*yaml.Node, map[*yaml.Node]bool)
	collect = func(node *yaml.Node, seen map[*yaml.Node]bool) {
		node = derefYAMLNode(node)
		if node == nil || node.Kind != yaml.MappingNode || seen[node] {
			return
		}
		seen[node] = true
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, ok := sqlcOutputValue(node.Content[i])
			if !ok {
				continue
			}
			if key == "<<" {
				for _, merged := range yamlMergedMappings(node.Content[i+1]) {
					collect(merged, seen)
				}
				continue
			}
			keys[key] = struct{}{}
		}
	}
	collect(node, map[*yaml.Node]bool{})
	result := make([]string, 0, len(keys))
	for key := range keys {
		result = append(result, key)
	}
	return result
}

func yamlNodeNonEmpty(node *yaml.Node) bool {
	node = derefYAMLNode(node)
	if node == nil || node.Tag == "!!null" {
		return false
	}
	switch node.Kind {
	case yaml.SequenceNode, yaml.MappingNode:
		return len(node.Content) > 0
	case yaml.ScalarNode:
		return node.Value != ""
	default:
		return true
	}
}

func sqlcOutputValue(node *yaml.Node) (string, bool) {
	seen := map[*yaml.Node]bool{}
	for node != nil && node.Kind == yaml.AliasNode && !seen[node] {
		seen[node] = true
		node = node.Alias
	}
	if node == nil || node.Kind != yaml.ScalarNode || node.Tag == "!!null" {
		return "", false
	}
	var value string
	if err := node.Decode(&value); err != nil {
		return "", false
	}
	return value, true
}

func derefYAMLNode(node *yaml.Node) *yaml.Node {
	seen := map[*yaml.Node]bool{}
	for node != nil && node.Kind == yaml.AliasNode && !seen[node] {
		seen[node] = true
		node = node.Alias
	}
	return node
}

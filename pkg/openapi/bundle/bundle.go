package bundle

import (
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"
)

const VirtualBundleName = ".anclax-bundle.yaml"

type Source struct {
	Bytes       []byte
	InputPath   string
	VirtualPath string
	SourceFiles []string
}

type merger struct {
	workdir               string
	merged                map[string]any
	singletonOwners       map[string]string
	pathFieldOwners       map[string]string
	componentOwners       map[string]string
	extensionOwners       map[string]string
	extensionSectionKinds map[string]string
	extensionSectionOwner map[string]string
}

func Prepare(workdir, inputPath string) (*Source, error) {
	resolvedInputPath, err := resolvePath(workdir, inputPath)
	if err != nil {
		return nil, err
	}

	info, err := os.Stat(resolvedInputPath)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		raw, err := os.ReadFile(resolvedInputPath)
		if err != nil {
			return nil, err
		}
		return &Source{
			Bytes:       normalizeRefBytes(raw),
			InputPath:   resolvedInputPath,
			VirtualPath: resolvedInputPath,
			SourceFiles: []string{resolvedInputPath},
		}, nil
	}

	files, err := collectYAMLFiles(resolvedInputPath)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, errors.Errorf("no OpenAPI yaml files found in %s", inputPath)
	}

	virtualPath := filepath.Join(resolvedInputPath, VirtualBundleName)
	m := merger{
		workdir:               filepath.Clean(workdir),
		merged:                map[string]any{},
		singletonOwners:       map[string]string{},
		pathFieldOwners:       map[string]string{},
		componentOwners:       map[string]string{},
		extensionOwners:       map[string]string{},
		extensionSectionKinds: map[string]string{},
		extensionSectionOwner: map[string]string{},
	}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		var doc map[string]any
		if err := yaml.Unmarshal(normalizeRefBytes(raw), &doc); err != nil {
			return nil, errors.Wrapf(err, "failed to parse %s", displayPath(workdir, file))
		}
		if doc == nil {
			doc = map[string]any{}
		}
		rewriteRefs(doc, file, virtualPath)
		if err := m.mergeFile(doc, file); err != nil {
			return nil, err
		}
	}

	buf, err := yaml.Marshal(m.merged)
	if err != nil {
		return nil, err
	}
	return &Source{
		Bytes:       normalizeRefBytes(buf),
		InputPath:   resolvedInputPath,
		VirtualPath: virtualPath,
		SourceFiles: files,
	}, nil
}

func Load(workdir, inputPath string) (*openapi3.T, string, error) {
	source, err := Prepare(workdir, inputPath)
	if err != nil {
		return nil, "", err
	}

	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = true
	rootURIPath := filepath.ToSlash(source.VirtualPath)
	loader.ReadFromURIFunc = func(loader *openapi3.Loader, location *url.URL) ([]byte, error) {
		if location != nil && location.Path == rootURIPath {
			return source.Bytes, nil
		}
		data, err := openapi3.DefaultReadFromURI(loader, location)
		if err != nil {
			return nil, err
		}
		return normalizeRefBytes(data), nil
	}

	baseURL := &url.URL{Path: rootURIPath}
	doc, err := loader.LoadFromDataWithPath(source.Bytes, baseURL)
	if err != nil {
		return nil, "", err
	}
	return doc, source.VirtualPath, nil
}

func (m *merger) mergeFile(doc map[string]any, file string) error {
	for _, key := range sortedKeys(doc) {
		value := doc[key]
		switch {
		case key == "paths":
			if err := m.mergePaths(value, file); err != nil {
				return err
			}
		case key == "components":
			if err := m.mergeComponents(value, file); err != nil {
				return err
			}
		case strings.HasPrefix(key, "x-"):
			if err := m.mergeExtension(key, value, file); err != nil {
				return err
			}
		default:
			if err := m.mergeSingleton(key, value, file); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *merger) mergeSingleton(key string, value any, file string) error {
	if previous, ok := m.singletonOwners[key]; ok {
		return duplicateError("top-level "+key, previous, file)
	}
	m.singletonOwners[key] = file
	m.merged[key] = value
	return nil
}

func (m *merger) mergePaths(value any, file string) error {
	paths, err := asMap(value, "paths")
	if err != nil {
		return errors.Wrapf(err, "in %s", displayPath(m.workdir, file))
	}
	dst, _ := m.merged["paths"].(map[string]any)
	if dst == nil {
		dst = map[string]any{}
		m.merged["paths"] = dst
	}

	for _, path := range sortedKeys(paths) {
		item, err := asMap(paths[path], "paths."+path)
		if err != nil {
			return errors.Wrapf(err, "in %s", displayPath(m.workdir, file))
		}
		dstItem, _ := dst[path].(map[string]any)
		if dstItem == nil {
			dstItem = map[string]any{}
			dst[path] = dstItem
		}
		for _, field := range sortedKeys(item) {
			ownerKey := path + "\x00" + field
			if previous, ok := m.pathFieldOwners[ownerKey]; ok {
				if method := httpMethodName(field); method != "" {
					return duplicateError("operation "+method+" "+path, previous, file)
				}
				return duplicateError("path field "+field+" for "+path, previous, file)
			}
			m.pathFieldOwners[ownerKey] = file
			dstItem[field] = item[field]
		}
	}
	return nil
}

func (m *merger) mergeComponents(value any, file string) error {
	components, err := asMap(value, "components")
	if err != nil {
		return errors.Wrapf(err, "in %s", displayPath(m.workdir, file))
	}
	dst, _ := m.merged["components"].(map[string]any)
	if dst == nil {
		dst = map[string]any{}
		m.merged["components"] = dst
	}

	for _, section := range sortedKeys(components) {
		if strings.HasPrefix(section, "x-") {
			if err := m.mergeNestedExtension("components."+section, section, components[section], dst, file); err != nil {
				return err
			}
			continue
		}
		sectionMap, err := asMap(components[section], "components."+section)
		if err != nil {
			return errors.Wrapf(err, "in %s", displayPath(m.workdir, file))
		}
		dstSection, _ := dst[section].(map[string]any)
		if dstSection == nil {
			dstSection = map[string]any{}
			dst[section] = dstSection
		}
		for _, name := range sortedKeys(sectionMap) {
			ownerKey := section + "\x00" + name
			if previous, ok := m.componentOwners[ownerKey]; ok {
				return duplicateError("component "+section+"."+name, previous, file)
			}
			m.componentOwners[ownerKey] = file
			dstSection[name] = sectionMap[name]
		}
	}
	return nil
}

func (m *merger) mergeExtension(key string, value any, file string) error {
	return m.mergeNestedExtension(key, key, value, m.merged, file)
}

func (m *merger) mergeNestedExtension(displayKey, dstKey string, value any, dst map[string]any, file string) error {
	items, err := asMap(value, displayKey)
	if err != nil {
		if previous, ok := m.extensionSectionOwner[displayKey]; ok {
			return duplicateError(displayKey, previous, file)
		}
		m.extensionSectionKinds[displayKey] = "scalar"
		m.extensionSectionOwner[displayKey] = file
		dst[dstKey] = value
		return nil
	}
	if previousKind, ok := m.extensionSectionKinds[displayKey]; ok && previousKind != "map" {
		return duplicateError(displayKey, m.extensionSectionOwner[displayKey], file)
	}
	if _, ok := m.extensionSectionKinds[displayKey]; !ok {
		m.extensionSectionKinds[displayKey] = "map"
		m.extensionSectionOwner[displayKey] = file
	}
	current, _ := dst[dstKey].(map[string]any)
	if current == nil {
		current = map[string]any{}
		dst[dstKey] = current
	}
	for _, name := range sortedKeys(items) {
		ownerKey := displayKey + "\x00" + name
		if previous, ok := m.extensionOwners[ownerKey]; ok {
			return duplicateError(displayKey+"."+name, previous, file)
		}
		m.extensionOwners[ownerKey] = file
		current[name] = items[name]
	}
	return nil
}

func collectYAMLFiles(root string) ([]string, error) {
	var files []string
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}
		files = append(files, filepath.Clean(path))
		return nil
	}); err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func rewriteRefs(value any, currentFile, bundleFile string) {
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			if key == "$ref" {
				ref, ok := nested.(string)
				if ok {
					typed[key] = rewriteRef(ref, currentFile, bundleFile)
				}
				continue
			}
			rewriteRefs(nested, currentFile, bundleFile)
		}
	case []any:
		for _, item := range typed {
			rewriteRefs(item, currentFile, bundleFile)
		}
	}
}

func rewriteRef(ref, currentFile, bundleFile string) string {
	ref = normalizeRefString(ref)
	if ref == "" || strings.HasPrefix(ref, "#") {
		return ref
	}
	if strings.Contains(ref, "://") || strings.HasPrefix(ref, "//") {
		return ref
	}

	refPath, fragment := splitRef(ref)
	if refPath == "" || filepath.IsAbs(filepath.FromSlash(refPath)) {
		return ref
	}
	targetPath := filepath.Clean(filepath.Join(filepath.Dir(currentFile), filepath.FromSlash(refPath)))
	rewrittenPath, err := filepath.Rel(filepath.Dir(bundleFile), targetPath)
	if err != nil {
		return ref
	}
	return joinRef(filepath.ToSlash(rewrittenPath), fragment)
}

func splitRef(ref string) (string, string) {
	parts := strings.SplitN(ref, "#", 2)
	if len(parts) == 1 {
		return parts[0], ""
	}
	return parts[0], parts[1]
}

func joinRef(refPath, fragment string) string {
	if fragment == "" {
		return refPath
	}
	return refPath + "#" + fragment
}

func resolvePath(workdir, inputPath string) (string, error) {
	if inputPath == "" {
		return "", errors.New("OpenAPI input path is required")
	}
	if filepath.IsAbs(inputPath) {
		return filepath.Clean(inputPath), nil
	}
	if workdir == "" {
		workdir = "."
	}
	return filepath.Clean(filepath.Join(workdir, inputPath)), nil
}

func asMap(value any, field string) (map[string]any, error) {
	if value == nil {
		return map[string]any{}, nil
	}
	ret, ok := value.(map[string]any)
	if !ok {
		return nil, errors.Errorf("%s must be a map", field)
	}
	return ret, nil
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func duplicateError(what, previousFile, currentFile string) error {
	return errors.Errorf("duplicate %s in %s and %s", what, previousFile, currentFile)
}

func displayPath(workdir, path string) string {
	if workdir == "" {
		return filepath.ToSlash(path)
	}
	rel, err := filepath.Rel(workdir, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

func httpMethodName(field string) string {
	switch strings.ToLower(field) {
	case "get", "put", "post", "delete", "options", "head", "patch", "trace":
		return strings.ToUpper(field)
	default:
		return ""
	}
}

func normalizeRefBytes(raw []byte) []byte {
	return []byte(strings.ReplaceAll(string(raw), "#schemas/", "#/schemas/"))
}

func normalizeRefString(ref string) string {
	return strings.Replace(ref, "#schemas/", "#/schemas/", 1)
}

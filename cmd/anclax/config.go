package main

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

type OapiCodegenConfig struct {
	Path    string `yaml:"path"`
	Out     string `yaml:"out"`
	Package string `yaml:"package"`
}

type SchemasConfig struct {
	Path   string `yaml:"path"`
	Output string `yaml:"output"`
}

type WireConfig struct {
	Path string `yaml:"path"`
}

type SqlcConfig struct {
	Path string `yaml:"path"`
}

type MockgenConfig struct {
	Files []MockgenFileConfig `yaml:"files"`
}

type MockgenFileConfig struct {
	Source      string `yaml:"source"`
	Destination string `yaml:"destination"`
	Package     string `yaml:"package"`
}

type TaskHandlerConfig struct {
	Package string `yaml:"package"`
	Out     string `yaml:"out"`
	Path    string `yaml:"path"`
}

type DSTConfig struct {
	Path    string `yaml:"path"`
	Out     string `yaml:"out"`
	Package string `yaml:"package,omitempty"`
}

type OapiCodegenConfigs []OapiCodegenConfig

type WireConfigs []WireConfig

type SqlcConfigs []SqlcConfig

type TaskHandlerConfigs []TaskHandlerConfig

func decodeSingleOrSlice[T any](value *yaml.Node, out *[]T, field string) error {
	if value.Tag == "!!null" {
		return nil
	}

	switch value.Kind {
	case yaml.MappingNode:
		var item T
		if err := value.Decode(&item); err != nil {
			return err
		}
		*out = []T{item}
		return nil
	case yaml.SequenceNode:
		var items []T
		if err := value.Decode(&items); err != nil {
			return err
		}
		*out = items
		return nil
	case 0:
		return nil
	default:
		return fmt.Errorf("%s must be a mapping or sequence", field)
	}
}

func (c *OapiCodegenConfigs) UnmarshalYAML(value *yaml.Node) error {
	return decodeSingleOrSlice(value, (*[]OapiCodegenConfig)(c), "oapi-codegen")
}

func (c *WireConfigs) UnmarshalYAML(value *yaml.Node) error {
	return decodeSingleOrSlice(value, (*[]WireConfig)(c), "wire")
}

func (c *SqlcConfigs) UnmarshalYAML(value *yaml.Node) error {
	return decodeSingleOrSlice(value, (*[]SqlcConfig)(c), "sqlc")
}

func (c *TaskHandlerConfigs) UnmarshalYAML(value *yaml.Node) error {
	return decodeSingleOrSlice(value, (*[]TaskHandlerConfig)(c), "task-handler")
}

type Config struct {
	Externals   map[string]string  `yaml:"externals,omitempty"`
	Schemas     *SchemasConfig     `yaml:"schemas,omitempty"`
	OapiCodegen OapiCodegenConfigs `yaml:"oapi-codegen,omitempty"`
	Wire        WireConfigs        `yaml:"wire,omitempty"`
	Mockgen     *MockgenConfig     `yaml:"mockgen,omitempty"`
	Sqlc        SqlcConfigs        `yaml:"sqlc,omitempty"`
	TaskHandler TaskHandlerConfigs `yaml:"task-handler,omitempty"`
	DST         []DSTConfig        `yaml:"dst,omitempty"`
	CleanItems  []string           `yaml:"clean,omitempty"`
	AnclaxDef   string             `yaml:"anclaxdef,omitempty"`
}

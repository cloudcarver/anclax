package main

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

type Config struct {
	Externals   map[string]string  `yaml:"externals,omitempty"`
	Schemas     *SchemasConfig     `yaml:"schemas,omitempty"`
	OapiCodegen *OapiCodegenConfig `yaml:"oapi-codegen,omitempty"`
	Wire        *WireConfig        `yaml:"wire,omitempty"`
	Mockgen     *MockgenConfig     `yaml:"mockgen,omitempty"`
	Sqlc        *SqlcConfig        `yaml:"sqlc,omitempty"`
	TaskHandler *TaskHandlerConfig `yaml:"task-handler,omitempty"`
	DST         []DSTConfig        `yaml:"dst,omitempty"`
	CleanItems  []string           `yaml:"clean,omitempty"`
	AnclaxDef   string             `yaml:"anclaxdef,omitempty"`
}

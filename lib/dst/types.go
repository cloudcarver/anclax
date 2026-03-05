package dst

type HybridSpec struct {
	Version    string                         `yaml:"version"`
	Package    string                         `yaml:"package,omitempty"`
	Interfaces map[string]HybridInterfaceSpec `yaml:"interfaces"`
	Instances  map[string]string              `yaml:"instances"`
	Scenarios  []HybridScenario               `yaml:"scenarios"`
}

type HybridInterfaceSpec struct {
	Methods []string `yaml:"methods"`
}

type HybridScenario struct {
	Name        string       `yaml:"name"`
	Description string       `yaml:"description,omitempty"`
	Steps       []HybridStep `yaml:"steps"`
}

type HybridStep struct {
	ID       string              `yaml:"id"`
	Parallel map[string][]string `yaml:"parallel"`
	Script   string              `yaml:"script,omitempty"`
}

type ParsedMethod struct {
	Raw     string
	Name    string
	Params  []ParsedParam
	Returns string
}

type ParsedParam struct {
	Name string
	Type string
}

type ParsedCall struct {
	Raw    string
	Method string
	Args   []string
}

const HybridVersionV1Alpha1 = "dst/hybrid/v1alpha1"

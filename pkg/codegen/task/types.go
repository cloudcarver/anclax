package codegen

type Field struct {
	Description string `yaml:"description"`
	Name        string `yaml:"name"`
	Type        string `yaml:"type"`
	Tag         string `yaml:"tag"`
}

type StructTemplateVars struct {
	StructName string  `yaml:"structName"`
	Fields     []Field `yaml:"fields"`
}

type Cronjob struct {
	CronExpression string `yaml:"cronExpression"`
}

type RetryPolicy struct {
	Interval             string `yaml:"interval"`
	AlwaysRetryOnFailure bool   `yaml:"alwaysRetryOnFailure"`
}

type Events struct {
	OnFailed *string `yaml:"onFailed,omitempty"`
}

type Function struct {
	Name          string       `yaml:"name"`
	Description   string       `yaml:"description"`
	ParameterType string       `yaml:"parameterType"`
	Timeout       *string      `yaml:"timeout,omitempty"`
	Cronjob       *Cronjob     `yaml:"cronjob,omitempty"`
	RetryPolicy   *RetryPolicy `yaml:"retryPolicy,omitempty"`
	Delay         *string      `yaml:"delay,omitempty"`
	Events        *Events      `yaml:"events,omitempty"`
}

type CodeTemplateVars struct {
	PackageName string
	StructDefs  string
	Functions   []Function
}

package codegen

type Field struct {
	Description string
	Name        string
	Type        string
	Tag         string
}

type StructTemplateVars struct {
	StructName string
	Fields     []Field
}

type Cronjob struct {
	CronExpression string
}

type RetryPolicy struct {
	Interval             string
	AlwaysRetryOnFailure bool
}

type Function struct {
	Name          string
	Description   string
	ParameterType string
	Timeout       *string
	Cronjob       *Cronjob
	RetryPolicy   *RetryPolicy
	Delay         *string
}

type CodeTemplateVars struct {
	PackageName string
	StructDefs  string
	Functions   []Function
}

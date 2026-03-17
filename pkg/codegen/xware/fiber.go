package codegen

import (
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/pkg/errors"
)

func Generate(workdir, packageName string, specPath string, outPath string) error {
	return errors.New("xware is deprecated; use the unified oapi generator")
}

func GenerateCode(doc *openapi3.T, packageName string, initialismOverrides bool) (string, error) {
	return "", errors.New("xware is deprecated; use the unified oapi generator")
}

package dst

import (
	"os"

	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"
)

func LoadHybridSpecFromFile(path string) (*HybridSpec, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.Wrapf(err, "read hybrid spec file %s", path)
	}
	return ParseHybridSpec(raw)
}

func ParseHybridSpec(raw []byte) (*HybridSpec, error) {
	var spec HybridSpec
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		return nil, errors.Wrap(err, "unmarshal hybrid spec yaml")
	}
	return &spec, nil
}

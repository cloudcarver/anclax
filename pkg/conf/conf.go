package conf

import (
	"os"
	"strconv"
	"strings"

	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"
)

// FetchConfig reads the config from the given path and environment variables.
// The config file should be in YAML format. If the value is both set in the config file and
// environment variables, the value in the environment variables will be used.
//
// Parameters:
//
// (optional) configPath. If it is empty, then reading from the file will be skipped.
//
// (optional) envPrefix. If it is empty, then "CFG" will be used as the default prefix.
//
// Note:
//
// The environment variables should be prefixed with `envPrefix`. e.g. `envPrefix` = "CFG",
// the environment variable should be CFG_PORT. Note that the underline here is used to separate the keys.
// So the environment variable CFG_PG_HOST will be parsed to the config file as pg.host.
// You should use `CFG_AuthorizedKey` not `CFG_AUTHORIZED_KEY` if you want to set the value of `authorizedKey`.
func FetchConfig(configPath string, envPrefix string, cfg any) error {
	prefix := "CFG"
	if len(envPrefix) != 0 {
		prefix = envPrefix
	}
	yamlRaw, err := readConfigFromPathAndEnv(prefix, configPath)
	if err != nil {
		return errors.Wrap(err, "failed to read and patch config")
	}
	return marshallRawYAML(yamlRaw, cfg)
}

func marshallRawYAML(yamlRaw []byte, cfg any) error {
	err := yaml.Unmarshal(yamlRaw, cfg)
	if err != nil {
		return errors.Wrapf(err, "failed to unmarshal yaml config %v", yamlRaw)
	}
	return nil
}

func readConfigFromPathAndEnv(prefix, configPath string) ([]byte, error) {
	config := map[string]any{}
	var err error
	if len(configPath) != 0 {
		config, err = readFromConfigFile(configPath)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to read config from %v", configPath)
		}
	}

	configEnv := readFromConfigEnv(prefix)

	if err := patchConfigMap(configEnv, config); err != nil {
		return nil, errors.Wrap(err, "failed to patch config env to config file")
	}
	yamlRaw, err := yaml.Marshal(config)
	if err != nil {
		return nil, errors.Wrap(err, "yaml marshal error")
	}
	return yamlRaw, nil
}

// patchConfigMap partially validates that both patch and base, then merge patch into base.
func patchConfigMap(patch, base map[string]any) error {
	if err := patchMap(base, patch); err != nil {
		return errors.Wrap(err, "failed to patch to config file")
	}
	return nil
}

func readFromConfigFile(configPath string) (map[string]any, error) {
	config := map[string]any{}

	_, err := os.Stat(configPath)
	if err != nil {
		return nil, errors.Wrapf(err, "config file %s not found", configPath)
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to read config file %s", configPath)
	}
	err = yaml.Unmarshal(raw, &config)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to unmarshal config file %s", configPath)
	}

	return config, nil
}

func readFromConfigEnv(prefix string) map[string]any {
	envCfg := map[string]any{}
	for _, v := range os.Environ() {
		if strings.HasPrefix(v, prefix) {
			key := strings.Split(v, "=")[0]
			value := v[len(key)+1:]
			key = strings.ToLower(strings.Replace(key, prefix, "", 1))
			parseEnvConfig(envCfg, key, value)
		}
	}
	return envCfg
}

// parseEnvConfig turn an environment variable to a map
// by convention, the env key has pattern A_B_C with each yaml config key separated by _
// calling function with key=MGMT_LOG_LEVEL and value=INFO
// should update curCfg to {MGMT: {LOG: [LEVEL: INFO}}}.
func parseEnvConfig(curCfg map[string]any, key string, value string) {
	i := strings.Index(key, "_")
	if i == -1 {
		if intVal, err := strconv.Atoi(value); err == nil {
			curCfg[key] = intVal
		} else if boolVal, err := strconv.ParseBool(value); err == nil {
			curCfg[key] = boolVal
		} else {
			curCfg[key] = value
		}
	} else {
		thisKey := key[:i]
		if _, ok := curCfg[thisKey]; !ok {
			curCfg[thisKey] = map[string]any{}
		}
		parseEnvConfig(curCfg[thisKey].(map[string]any), key[i+1:], value)
	}
}

func patchMap(o map[string]any, p map[string]any) error {
	for k := range p {
		if _, ok := o[k]; ok { // if o has the same key
			if _, ok := o[k].(map[string]any); ok {
				if _, ok := p[k].(map[string]any); !ok {
					return errors.Errorf("%s of %s is not a map[string]any", k, p)
				}
				// o[k] and p[k] are both map
				if err := patchMap(o[k].(map[string]any), p[k].(map[string]any)); err != nil {
					return err
				}
			} else { // both are values
				o[k] = p[k]
			}
		} else { // o does not have this key
			o[k] = p[k]
		}
	}
	return nil
}

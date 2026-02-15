package dst

import "github.com/pkg/errors"

func ValidateHybridSpec(spec *HybridSpec) error {
	if spec == nil {
		return errors.New("spec is nil")
	}
	if spec.Version == "" {
		return errors.New("version is required")
	}
	if spec.Version != HybridVersionV1Alpha1 {
		return errors.Errorf("unsupported hybrid version %q", spec.Version)
	}
	if len(spec.Interfaces) == 0 {
		return errors.New("interfaces is required")
	}
	if len(spec.Instances) == 0 {
		return errors.New("instances is required")
	}
	if len(spec.Scenarios) == 0 {
		return errors.New("at least one scenario is required")
	}

	parsedMethodsByInterface := map[string]map[string]ParsedMethod{}
	for name, iface := range spec.Interfaces {
		if name == "" {
			return errors.New("interface name cannot be empty")
		}
		if len(iface.Methods) == 0 {
			return errors.Errorf("interface %q must declare at least one method", name)
		}
		parsedMethodsByInterface[name] = map[string]ParsedMethod{}
		for i, rawSig := range iface.Methods {
			parsed, err := ParseMethodSignature(rawSig)
			if err != nil {
				return errors.Wrapf(err, "interface %q method[%d]", name, i)
			}
			if _, ok := parsedMethodsByInterface[name][parsed.Name]; ok {
				return errors.Errorf("duplicate method %q in interface %q", parsed.Name, name)
			}
			parsedMethodsByInterface[name][parsed.Name] = parsed
		}
	}

	for instanceName, interfaceName := range spec.Instances {
		if instanceName == "" {
			return errors.New("instance name cannot be empty")
		}
		if interfaceName == "" {
			return errors.Errorf("instance %q has empty interface", instanceName)
		}
		if _, ok := spec.Interfaces[interfaceName]; !ok {
			return errors.Errorf("instance %q references unknown interface %q", instanceName, interfaceName)
		}
	}

	scenarioNames := map[string]struct{}{}
	for si, scenario := range spec.Scenarios {
		if scenario.Name == "" {
			return errors.Wrapf(errors.New("name is required"), "scenario[%d]", si)
		}
		if _, ok := scenarioNames[scenario.Name]; ok {
			return errors.Errorf("duplicate scenario name %q", scenario.Name)
		}
		scenarioNames[scenario.Name] = struct{}{}
		if len(scenario.Steps) == 0 {
			return errors.Errorf("scenario %q requires at least one step", scenario.Name)
		}
		stepIDs := map[string]struct{}{}
		for ti, step := range scenario.Steps {
			if step.ID == "" {
				return errors.Errorf("scenario %q step[%d] requires id", scenario.Name, ti)
			}
			if _, ok := stepIDs[step.ID]; ok {
				return errors.Errorf("scenario %q has duplicate step id %q", scenario.Name, step.ID)
			}
			stepIDs[step.ID] = struct{}{}
			if len(step.Parallel) == 0 {
				return errors.Errorf("scenario %q step %q requires parallel entries", scenario.Name, step.ID)
			}
			for actorInstance, calls := range step.Parallel {
				if _, ok := spec.Instances[actorInstance]; !ok {
					return errors.Errorf("scenario %q step %q references unknown actor instance %q", scenario.Name, step.ID, actorInstance)
				}
				if len(calls) == 0 {
					return errors.Errorf("scenario %q step %q actor %q has empty call list", scenario.Name, step.ID, actorInstance)
				}
				ifaceName := spec.Instances[actorInstance]
				methodSet := parsedMethodsByInterface[ifaceName]
				for ci, callRaw := range calls {
					call, err := ParseCallExpression(callRaw)
					if err != nil {
						return errors.Wrapf(err, "scenario %q step %q actor %q call[%d]", scenario.Name, step.ID, actorInstance, ci)
					}
					method, ok := methodSet[call.Method]
					if !ok {
						return errors.Errorf("scenario %q step %q actor %q call %q references unknown method %q for interface %q", scenario.Name, step.ID, actorInstance, callRaw, call.Method, ifaceName)
					}
					if len(call.Args) != len(method.Params) {
						return errors.Errorf("scenario %q step %q actor %q call %q argument count mismatch: got %d want %d", scenario.Name, step.ID, actorInstance, callRaw, len(call.Args), len(method.Params))
					}
				}
			}
		}
	}
	return nil
}

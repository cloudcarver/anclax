
# Application config

Use application configuration to control runtime behavior (ports, timeouts, feature flags, credentials). Keep secrets out of source control and rely on environment variables or secret managers. Support both config files and environment variable overrides.

## Principles

- Prefer explicit config over magic defaults.
- Keep config names stable once shipped.
- Validate required config at startup.
- Separate build-time from runtime configuration.
- Ensure config fields are easy to override via environment variables.

## Env override rules

- Config struct tags should use camelCase without `_` (example: `yaml:"secretKey"`).
- Environment variables are formed by uppercasing the key and prefixing with `MYAPP_`.
- Example: `yaml:"secretKey"` maps to `MYAPP_SECRETKEY`.

## What to document

- Each config key, its type, and default.
- Whether it is required or optional.
- Where it is read in the code (file or package).
- Any security considerations (secret, PII, external endpoint).

## Operational checklist

1. Add new keys with safe defaults.
2. Wire config into the relevant service/component.
3. Update docs and examples.
4. Verify startup validation and error messages.

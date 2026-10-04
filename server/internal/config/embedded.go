package config

import (
	_ "embed"
)

//go:embed embedded_versions.toml
var EmbeddedVersionsTOML string

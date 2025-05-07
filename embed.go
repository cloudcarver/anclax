package anchor

import "embed"

//go:embed sql/migrations/*
var Migrations embed.FS

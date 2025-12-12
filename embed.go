package anclax

import "embed"

//go:embed sql/*
var Migrations embed.FS

//go:embed api/*
var API embed.FS

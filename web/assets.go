package webassets

import "embed"

// Assets embeds the default web UI so the binary can run standalone.
//
//go:embed templates/*.html static/*
var Assets embed.FS

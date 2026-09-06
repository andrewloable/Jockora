// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

// Package web holds the player page and its vendored dependencies, embedded so
// that the server is a single binary with no runtime asset path.
package web

import "embed"

// Files holds index.html, admin.html and everything under vendor/.
//
// vendor/hls.light.min.js is hls.js v1.5.17, Apache-2.0, with its licence
// alongside it in vendor/hls.js.LICENSE. It is vendored rather than loaded from
// a CDN because this is a self-hosted product that has to work on a machine
// with no internet connection.
//
//go:embed index.html admin.html vendor
var Files embed.FS

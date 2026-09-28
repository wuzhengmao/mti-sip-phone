// Package web 内嵌网页资源：测试页面与 JS SDK，随二进制一起分发（开箱即用）。
package web

import "embed"

// Assets 内嵌资源根：test.html 与 sdk/ 目录。
//
//go:embed test.html sdk/*
var Assets embed.FS

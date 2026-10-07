// Package web 内嵌网页资源：网页控制台（单页壳 + 多视图）与 JS SDK，
// 随二进制一起分发（开箱即用）。
package web

import "embed"

// Assets 内嵌资源根：控制台入口页、样式、脚本与 sdk/ 目录。
//
// 改动这些资源后**必须重新构建 + 重启软电话**才生效（资源是编进二进制的），
// 前端出问题时先确认跑的是不是新构建。
//
//go:embed index.html css/* js/* js/views/* sdk/*
var Assets embed.FS

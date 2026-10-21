// Package version 持有构建版本号，由构建期 -ldflags -X 注入。
package version

// Version 默认 dev；正式构建经 `git describe --tags --always` 回填，
// goreleaser 构建时由 {{.Version}} 回填。
var Version = "dev"

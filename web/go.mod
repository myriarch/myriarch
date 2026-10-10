// This nested module only exists to exclude web/ (including node_modules,
// which may contain stray .go files) from the root Go module, so that
// `go build ./...` and `go vet ./...` never descend into it.
module github.com/myriarch/myriarch/web-excluded

go 1.26.0

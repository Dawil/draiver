// evals/ is not Go code. This stub module fences it off from the root module, so that
// `go test ./...` (the unit rung) doesn't descend into evals/node_modules, where
// promptfoo ships .go files that don't build on their own (drv-065).
module github.com/Dawil/draiver/evals

go 1.22

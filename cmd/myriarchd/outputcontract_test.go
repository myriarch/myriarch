// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myriarch/myriarch/internal/brand"
	"github.com/myriarch/myriarch/internal/testutil/clitest"
)

// TestOutputContract runs the real main with the real os.Stdout and os.Stderr: the version and help go to stdout,
// errors to stderr (docs/development.md, "CLI output").
func TestOutputContract(t *testing.T) { clitest.CheckOutputContract(t, brand.ServerBinary) }

// TestNoAmbiguousPrintHelpers refuses cobra's cmd.Print, cmd.Println and cmd.Printf (they write to stderr unless a
// writer was set) and fmt.Print, fmt.Println and fmt.Printf (they bypass the writers of the command) in the commands
// of the four binaries. Results go to fmt.Fprint*(cmd.OutOrStdout(), ...), diagnostics to cmd.ErrOrStderr().
func TestNoAmbiguousPrintHelpers(t *testing.T) {
	banned := map[string]bool{"Print": true, "Println": true, "Printf": true, "PrintErr": true, "PrintErrln": true, "PrintErrf": true}
	var hits []string
	for _, dir := range []string{"../../cmd", "../../internal/updater", "../../internal/sim", "../../internal/myrctl"} {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !banned[sel.Sel.Name] {
					return true
				}
				// a logger (log, slog, a *slog.Logger field) is fine: its output is not command output
				if id, ok := sel.X.(*ast.Ident); ok && (id.Name == "log" || id.Name == "slog") {
					return true
				}
				hits = append(hits, fset.Position(call.Pos()).String()+": "+sel.Sel.Name)
				return true
			})
			return nil
		})
		require.NoError(t, err)
	}
	assert.Empty(t, hits, "use fmt.Fprint*(cmd.OutOrStdout(), ...) for results and cmd.ErrOrStderr() for diagnostics")
}

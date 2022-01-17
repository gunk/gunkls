package lint

import (
	"context"
	"go/ast"
	"go/token"

	"github.com/gunk/gunkls/lsp/loader"
	"go.lsp.dev/protocol"
)

func LintPkg(ctx context.Context, pkg *loader.GunkPackage, loader *loader.Loader) map[string][]protocol.Diagnostic {
	diagnostics := make(map[string][]protocol.Diagnostic)
	// commentstart
	for k, v := range commentStart(ctx, pkg, loader.Fset) {
		diagnostics[k] = append(diagnostics[k], v...)
	}
	return diagnostics
}

type node struct {
	pos token.Pos
	end token.Pos
}

func (n node) Pos() token.Pos {
	return n.pos
}

func (n node) End() token.Pos {
	return n.end
}

func lintWarning(file string, fset *token.FileSet, node ast.Node, msg string, code string) protocol.Diagnostic {
	startPos := fset.Position(node.Pos())
	endPos := fset.Position(node.End())
	return protocol.Diagnostic{
		Range: protocol.Range{
			Start: protocol.Position{
				Line:      uint32(startPos.Line) - 1,
				Character: uint32(startPos.Column) - 1,
			},
			End: protocol.Position{
				Line:      uint32(endPos.Line) - 1,
				Character: uint32(endPos.Column) - 1,
			},
		},
		Severity: 2,
		Source:   "gunkls",
		Message:  msg,
		Code:     code,
	}
}

package lsp

import (
	"context"
	"go/ast"
	"go/token"
	"strings"

	"github.com/gunk/gunkls/lsp/loader"
	"go.lsp.dev/protocol"
)

func (l *LSP) doLinting(ctx context.Context, pkg *loader.GunkPackage) map[string][]protocol.Diagnostic {
	if !l.lint {
		return nil
	}
	diagnostics := make(map[string][]protocol.Diagnostic)
	for i, f := range pkg.GunkSyntax {
		file := pkg.GunkFiles[i]
		ast.Inspect(f, func(n ast.Node) bool {
			var msg string
			var exists bool
			switch v := n.(type) {
			default:
				return false
			case *ast.GenDecl, *ast.StructType, *ast.InterfaceType, *ast.FieldList:
				return true
			case *ast.File:
				return true
			case *ast.TypeSpec:
				msg, exists = checkCommentStart(n, v.Name.Name, v.Doc.Text())
				if exists {
					n = v.Doc.List[0]
				} else {
					n = v.Name
				}
			case *ast.Field:
				if len(v.Names) != 1 {
					return true
				}
				msg, exists = checkCommentStart(n, v.Names[0].Name, v.Doc.Text())
				if exists {
					n = v.Doc.List[0]
				} else {
					n = v.Names[0]
				}
			}
			if msg != "" {
				diagnostics[file] = append(diagnostics[file], lintWarning(file, l.loader.Fset, n, msg, "commentstart"))
			}
			return true
		})
	}
	return diagnostics
}

// checkCommentStart checks the start of a comment for a name prefix.
// if an issue is found, the diagnostic message is returned, and whether
// the warning is on the comment, or the tip.
func checkCommentStart(n ast.Node, name string, comment string) (string, bool) {
	prefix := name + " "
	if strings.HasPrefix(comment, prefix) {
		return "", false
	}
	if comment == "" {
		return "missing comment", false
	}
	return "comment should start with '" + prefix + "'", true
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

package lint

import (
	"context"
	"go/ast"
	"go/token"
	"strings"

	"github.com/gunk/gunkls/lsp/loader"
	"go.lsp.dev/protocol"
)

func commentStart(ctx context.Context, pkg *loader.GunkPackage, fset *token.FileSet) map[string][]protocol.Diagnostic {
	diagnostics := make(map[string][]protocol.Diagnostic)
	for i, f := range pkg.GunkSyntax {
		file := pkg.GunkFiles[i]
		ast.Inspect(f, func(n ast.Node) bool {
			var msg string
			var exists bool
			switch v := n.(type) {
			default:
				return false
			case *ast.GenDecl, *ast.StructType, *ast.InterfaceType, *ast.FieldList, *ast.File:
				return true
			case *ast.TypeSpec:
				msg, exists = checkCommentStart(n, v.Name.Name, v.Doc.Text())
				if exists {
					n = toFirstWord(v.Doc.List[0])
				} else {
					n = v.Name
				}
			case *ast.Field:
				if len(v.Names) != 1 {
					return true
				}
				msg, exists = checkCommentStart(n, v.Names[0].Name, v.Doc.Text())
				if exists {
					n = toFirstWord(v.Doc.List[0])
				} else {
					n = v.Names[0]
				}
			}
			if msg != "" {
				diagnostics[file] = append(diagnostics[file], lintWarning(file, fset, n, msg, "commentstart"))
			}
			return true
		})
	}
	return diagnostics
}

// checkCommentStart checks the start of a comment for a name prefix.
// if an issue is found, the diagnostic message is returned, and whether
// the warning should be on the comment, or the type.
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

// toFirstWord modifies the ast.Comment's position so it only spans
// to the first word.
func toFirstWord(n *ast.Comment) ast.Node {
	str := strings.TrimSpace(n.Text[2:])
	missing := len(n.Text) - len(str)
	return node{
		pos: n.Slash,
		end: n.Slash + token.Pos(strings.IndexRune(str, ' ')+missing),
	}
}

package lsp

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"strconv"

	"github.com/gunk/gunkls/lsp/loader"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

var invalidType = errors.New("can only go to definition on struct or enum types")

func (l *LSP) Goto(ctx context.Context, params protocol.DefinitionParams, reply jsonrpc2.Replier) {
	file := params.TextDocument.URI.Filename()
	pkg, err := l.filePkg(file)
	if err != nil {
		reply(ctx, nil, err)
		return
	}
	// does this file have errors, or another file?
	var fileErr bool
	for _, err := range pkg.Errors {
		if err.File == file && err.Kind != loader.ValidateError {
			fileErr = true
			break
		}
	}
	if fileErr {
		reply(ctx, nil, fmt.Errorf("file %s has errors", file))
		return
	}
	// find the file
	var f *ast.File
	for i, path := range pkg.GunkFiles {
		if path == file {
			f = pkg.GunkSyntax[i]
			break
		}
	}
	if f == nil {
		reply(ctx, nil, fmt.Errorf("could not find file %s", file))
		return
	}
	// LSP params are 0 indexed
	pos := params.Position
	pos.Character++
	pos.Line++

	type bailout struct{}

	defer func() {
		x := recover()
		if x == nil {
			return
		}
		if _, ok := x.(bailout); ok {
			return
		}
		panic(x)
	}()

	var foundTyp bool
	ast.Inspect(f, func(node ast.Node) bool {
		switch node := node.(type) {
		default:
			return false
		case *ast.File, *ast.GenDecl, *ast.TypeSpec, *ast.FieldList, *ast.Field, *ast.StructType, *ast.InterfaceType:
			return contains(l.loader.Fset, node, pos)
		case *ast.ArrayType, *ast.FuncType, *ast.ChanType, *ast.MapType:
			if !contains(l.loader.Fset, node, pos) {
				return false
			}
			// Make a note that we are inside these types so we can notify the
			// user that they should place their cursor on the identifier.
			foundTyp = true
			return true
		case *ast.ImportSpec:
			if !contains(l.loader.Fset, node, pos) {
				return false
			}
			l.gotoImport(ctx, node, reply)
			panic(bailout{})
		case *ast.SelectorExpr, *ast.Ident:
			if !contains(l.loader.Fset, node, pos) {
				return false
			}
			// node must be an expression as it can only be selector or identifier.
			n := node.(ast.Expr)
			l.gotoType(ctx, pkg, n, reply)
			panic(bailout{})
		}
	})

	if foundTyp {
		reply(ctx, nil, invalidType)
		return
	}
	// Not a valid location to go to definition at.
	reply(ctx, nil, nil)
}

// gotoImport handles goto requests when the cursor is on an import path.
func (l *LSP) gotoImport(ctx context.Context, spec *ast.ImportSpec, reply jsonrpc2.Replier) {
	// Load the package specified.
	path, _ := strconv.Unquote(spec.Path.Value)
	pkgs, err := l.loader.Load(path)
	if err != nil || len(pkgs) > 1 {
		reply(ctx, nil, fmt.Errorf("unexpected error loading %q: %v", path, err))
		return
	}
	if len(pkgs) == 0 {
		reply(ctx, nil, fmt.Errorf("no gunk files in package %s", spec.Path.Value))
		return
	}
	// Create the list of files to reply with.
	pkg := pkgs[0]
	files := make([]protocol.Location, 0, len(pkg.GunkFiles))
	for _, v := range pkg.GunkFiles {
		files = append(files, protocol.Location{
			URI: uri.File(v),
		})
	}
	reply(ctx, files, nil)
}

// gotoIdent handles goto requests when the cursor is on a type.
func (l *LSP) gotoType(ctx context.Context, pkg *loader.GunkPackage, expr ast.Expr, reply jsonrpc2.Replier) {
	typAndValue := pkg.TypesInfo.Types[expr]
	if !typAndValue.IsType() {
		// Not a type. Ignore.
		reply(ctx, nil, nil)
		return
	}
	typ := typAndValue.Type
	switch typ := typ.(type) {
	default:
		reply(ctx, nil, fmt.Errorf("unknown type of type: %T", typ))
		return
	case *types.Basic:
		reply(ctx, nil, invalidType)
		return
	case *types.Named:
		pos := l.loader.Fset.Position(typ.Obj().Pos())
		if !pos.IsValid() {
			reply(ctx, nil, invalidType)
			return
		}
		loc := protocol.Location{
			URI: uri.File(pos.Filename),
			Range: protocol.Range{
				Start: protocol.Position{
					Line:      uint32(pos.Line - 1),
					Character: uint32(pos.Column - 1),
				},
				End: protocol.Position{
					Line:      uint32(pos.Line - 1),
					Character: uint32(pos.Column - 1),
				},
			},
		}
		reply(ctx, []protocol.Location{loc}, nil)
		return
	}
}

func contains(fset *token.FileSet, node ast.Node, pos protocol.Position) bool {
	start := fset.Position(node.Pos())
	end := fset.Position(node.End())

	if int(pos.Line) < start.Line || int(pos.Line) > end.Line {
		return false
	}
	if int(pos.Line) == start.Line && int(pos.Character) < start.Column {
		return false
	}
	if int(pos.Line) == end.Line && int(pos.Character) > end.Column {
		return false
	}
	return true
}

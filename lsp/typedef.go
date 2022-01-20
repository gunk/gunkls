package lsp

import (
	"context"
	"fmt"
	"go/ast"
	"go/token"
	"log"
	"strconv"
	"strings"

	"github.com/gunk/gunkls/lsp/loader"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

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

	var done bool
	ast.Inspect(f, func(node ast.Node) bool {
		if done {
			return false
		}

		switch node := node.(type) {
		default:
			return false
		case *ast.File, *ast.GenDecl, *ast.TypeSpec, *ast.StructType, *ast.InterfaceType, *ast.FieldList, *ast.FuncType:
			return true
		case *ast.ImportSpec:
			if !contains(l.loader.Fset, node, pos) {
				return false
			}
			done = true
			path, _ := strconv.Unquote(node.Path.Value)
			pkgs, err := l.loader.Load(path)
			if err != nil || len(pkgs) > 1 {
				reply(ctx, nil,
					fmt.Errorf("unexpected error loading %q: %v", path, err))
				return false
			}
			if len(pkgs) == 0 {
				l.msg(ctx, protocol.MessageTypeError,
					fmt.Sprintf("no gunk files in package %s", node.Path.Value))
				return false
			}

			var files []protocol.Location
			for _, v := range pkgs[0].GunkFiles {
				files = append(files, protocol.Location{
					URI: uri.File(v),
					Range: protocol.Range{
						Start: protocol.Position{
							Line:      0,
							Character: 0,
						},
						End: protocol.Position{
							Line:      0,
							Character: 0,
						},
					},
				})
			}
			reply(ctx, files, nil)
		case *ast.Field:
			if _, ok := node.Type.(*ast.FuncType); ok {
				return true
			}
			if !contains(l.loader.Fset, node.Type, pos) {
				return false
			}
			done = true
			if len(node.Names) == 0 {
				reply(ctx, nil,
					fmt.Errorf("no nodes found for %v", node))
				return false
			}
			name := pkg.TypesInfo.TypeOf(node.Names[0])
			if name == nil {
				reply(ctx, nil,
					fmt.Errorf("no type found for %v", node))
				return false
			}
			str := name.String()
			for strings.HasPrefix(str, "[]") {
				str = str[2:]
			}
			dot := strings.LastIndexByte(str, '.')
			if dot == -1 {
				reply(ctx, nil, fmt.Errorf("type is builtin"))
				// builtin
				return false
			}
			pkgName := str[:dot]
			typeName := str[dot+1:]

			if pkgName != pkg.Package.ID {
				pkgs, err := l.loader.Load(pkgName)
				if err != nil {
					reply(ctx, nil, fmt.Errorf("unexpected error loading %q: %v", pkgName, err))
					return false
				}
				if len(pkgs) != 1 {
					reply(ctx, nil, fmt.Errorf("unexpected error loading %q: %v", pkgName, err))
					return false
				}
				pkg = pkgs[0]
			}
			var done bool
			for i, f := range pkg.GunkSyntax {
				path := pkg.GunkFiles[i]
				log.Println("checking", path)
				ast.Inspect(f, func(n ast.Node) bool {
					if done {
						return false
					}
					switch n := n.(type) {
					default:
						return false
					case *ast.File, *ast.GenDecl:
						return true
					case *ast.TypeSpec:
						position := l.loader.Fset.Position(n.Pos())
						pos := protocol.Position{
							Line:      uint32(position.Line) - 1,
							Character: uint32(position.Column) - 1,
						}
						if n.Name.Name == typeName {
							reply(ctx, protocol.Location{
								URI: uri.File(path),
								Range: protocol.Range{
									Start: pos,
									End:   pos,
								},
							}, nil)
							return false
						}
					}
					return false
				})
				if done {
					break
				}
			}
		}
		return false
	})
}

func contains(fset *token.FileSet, node ast.Node, pos protocol.Position) bool {
	start := fset.Position(node.Pos())
	end := fset.Position(node.End())

	return int(pos.Line) >= start.Line &&
		int(pos.Line) <= end.Line &&
		int(pos.Character) >= start.Column &&
		int(pos.Character) <= end.Column
}

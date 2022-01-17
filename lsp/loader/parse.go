package loader

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"go/types"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"github.com/gunk/gunk/loader"
)

// ParsePackage parses the package's GunkFiles, and type-checks the package
// if l.Types is set.
func (l *Loader) ParsePackage(pkg *GunkPackage, checkTypes bool) {
	// Clear the name before parsing to avoid Go files from triggering package
	// name mismatch
	pkg.Name = ""
	l.cache[pkg.Dir] = pkg
	var badPkgName bool
	// parse the gunk files
	for _, fpath := range pkg.GunkFiles {
		var src interface{}
		if contents, ok := l.InMemoryFiles[fpath]; ok {
			src = contents
		}
		file, err := parser.ParseFile(l.Fset, fpath, src, parser.ParseComments)
		if err != nil {
			pkg.parseError(fpath, err)
			continue
		}
		// to make the generated code independent of the current
		// directory when running gunk
		relPath := pkg.PkgPath + "/" + filepath.Base(fpath)
		pkg.GunkNames = append(pkg.GunkNames, relPath)
		pkg.GunkSyntax = append(pkg.GunkSyntax, file)
		// Update pkg.name
		if name := file.Name.Name; pkg.Name == "" {
			pkg.Name = name
		} else if pkg.Name != name {
			badPkgName = true
		}
	}
	if badPkgName {
		for i, f := range pkg.GunkFiles {
			ast := pkg.GunkSyntax[i]
			from := ast.Name.NamePos
			to := from + token.Pos(len(ast.Name.Name))
			pkg.error(f, from, to, l.Fset, "found more than one package name", ValidateError)
		}
	}
	if pkg.ProtoName == "" {
		pkg.ProtoName = pkg.Name
	}
	// the reported error will be handled by Diagnostics
	if len(pkg.Errors) > 0 || !checkTypes {
		return
	}
	pkg.Types = types.NewPackage(pkg.PkgPath, pkg.Name)
	tconfig := &types.Config{
		DisableUnusedImportCheck: true,
		Importer:                 l,
		Error: func(e error) {
			if err, ok := e.(types.Error); ok {
				pos := err.Fset.Position(err.Pos)

				var file *ast.File
				for i, f := range pkg.GunkFiles {
					if f == pos.Filename {
						file = pkg.GunkSyntax[i]
						break
					}
				}
				if file == nil {
					pkg.addError(TypeError, err.Pos, err.Fset, errors.New(err.Msg))
					return
				}
				node := getNode(err.Pos, file)
				if node == nil {
					pkg.addError(TypeError, err.Pos, err.Fset, errors.New(err.Msg))
					return
				}
				pkg.error(pos.Filename, node.Pos(), node.End(), err.Fset, err.Msg, TypeError)
			}
		},
	}
	pkg.TypesInfo = &types.Info{
		Types:      make(map[ast.Expr]types.TypeAndValue),
		Defs:       make(map[*ast.Ident]types.Object),
		Uses:       make(map[*ast.Ident]types.Object),
		Implicits:  make(map[ast.Node]types.Object),
		Scopes:     make(map[ast.Node]*types.Scope),
		Selections: make(map[*ast.SelectorExpr]*types.Selection),
	}
	check := types.NewChecker(tconfig, l.Fset, pkg.Types, pkg.TypesInfo)
	err := check.Files(pkg.GunkSyntax)
	if err != nil {
		return
	}
	// Update import paths, so we can keep file up to date when an imported
	// file is changed.
	pkg.Imports = make(map[string]*loader.GunkPackage)
	for _, file := range pkg.GunkSyntax {
		l.splitGunkTags(pkg, file)
		for _, spec := range file.Imports {
			// we can't error, since the file parsed correctly
			pkgPath, _ := strconv.Unquote(spec.Path.Value)
			// it's legal to import a package that has errors
			pkgs, _ := l.Load(pkgPath)
			if len(pkgs) == 1 {
				pkg.Imports[pkgPath] = pkgs[0].GunkPackage
			}
		}
	}
}

// validatePackage sanity checks a gunk package, to find common errors which are
// shared among all gunk commands.
func (l *Loader) validatePackage(pkg *GunkPackage) {
	for i, file := range pkg.GunkSyntax {
		path := pkg.GunkFiles[i]
		ast.Inspect(file, func(node ast.Node) bool {
			st, ok := node.(*ast.StructType)
			if !ok || st.Fields == nil {
				return true
			}
			// Look through all fields for anonymous/unnamed types.
			for _, field := range st.Fields.List {
				if len(field.Names) < 1 {
					pkg.error(path, field.Pos(), field.End(), l.Fset, "anonymous struct fields are not supported", ParseError)
					return false
				}
			}
			// Check for struct tag 'pb' and ensure that if it does exist
			// it is a valid integer, and it is unique in that struct.
			// The other validation should happen in format and generate
			// as they both treat the same error cases differently.
			usedSequences := make(map[int]*ast.BasicLit, len(st.Fields.List))
			jsonNamesSeen := map[string]*ast.BasicLit{}
			for _, f := range st.Fields.List {
				tag := f.Tag
				if tag == nil {
					continue
				}
				str, _ := strconv.Unquote(f.Tag.Value)
				if err := validateStructTag(str); err != nil {
					pkg.error(path, tag.Pos(), tag.End(), l.Fset, err.Error(), ParseError)
					continue
				}
				stag := reflect.StructTag(str)
				val, ok := stag.Lookup("pb")
				if !ok || val == "" {
					continue
				}
				valJson, ok := stag.Lookup("json")
				if ok && valJson != "" {
					if jsonNamesSeen[valJson] != nil {
						msg := fmt.Sprintf("json tag %q seen twice", valJson)
						pkg.error(path, tag.Pos(), tag.End(), l.Fset, msg, ParseError)
						continue
					}
					jsonNamesSeen[valJson] = tag
				}
				sequence, err := strconv.Atoi(val)
				if err != nil {
					msg := fmt.Sprintf("invalid sequence number %q", val)
					pkg.error(path, tag.Pos(), tag.End(), l.Fset, msg, ParseError)
					continue
				}
				if usedSequences[sequence] != nil {
					msg := fmt.Sprintf("sequence number %q seen twice", val)
					pkg.error(path, tag.Pos(), tag.End(), l.Fset, msg, ParseError)
					continue
				}
				usedSequences[sequence] = tag
			}
			return true
		})
	}
}

// splitGunkTags parses and typechecks gunk tags from the comments in a Gunk
// file, adding them to pkg.GunkTags and removing the source lines from each
// comment.
func (l *Loader) splitGunkTags(pkg *GunkPackage, file *ast.File) {
	// hadError := false
	ast.Inspect(file, func(node ast.Node) bool {
		if gd, ok := node.(*ast.GenDecl); ok {
			if len(gd.Specs) != 1 {
				return true
			}
			if doc := nodeDoc(gd.Specs[0]); doc != nil {
				// Move the doc to the only spec, since we want
				// +gunk tags attached to the type specs.
				*doc = gd.Doc
			}
			return true
		}
		doc := nodeDoc(node)
		if doc == nil {
			return true
		}
		_, exprs, err := SplitGunkTag(pkg, l.Fset, *doc)
		if err != nil {
			// hadError = true
			pkg.addError(ValidateError, (*doc).Pos(), l.Fset, err)
			return false
		}
		if len(exprs) > 0 {
			if pkg.GunkTags == nil {
				pkg.GunkTags = make(map[ast.Node][]loader.GunkTag)
			}
			pkg.GunkTags[node] = exprs
			// **doc = *CommentFromText(*doc, docText)
		}
		return true
	})
	// if !hadError {
	// 	for _, cg := range file.Comments {
	// 		for _, c := range cg.List {
	// 			if strings.Contains(Text, "+gunk") {
	// 				pkg.errorf(ParseError, c.Pos(), l.Fset, "gunk tag without declaration: %s", c.Text)
	// 			}
	// 		}
	// 	}
	// }
}

func nodeDoc(node ast.Node) **ast.CommentGroup {
	switch node := node.(type) {
	case *ast.File:
		return &node.Doc
	case *ast.Field:
		return &node.Doc
	case *ast.TypeSpec:
		return &node.Doc
	case *ast.ValueSpec:
		return &node.Doc
	}
	return nil
}

// TODO(mvdan): both loader and format use CommentFromText, but it feels awkward
// to have it here.
// CommentFromText creates a multi-line comment from the given text, with its
// start and end positions matching the given node's.
func CommentFromText(orig ast.Node, text string) *ast.CommentGroup {
	group := &ast.CommentGroup{}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		comment := &ast.Comment{Text: "// " + line}
		// Ensure that group.Pos() and group.End() stay on the same
		// lines, to ensure that printing doesn't move the comment
		// around or introduce newlines.
		switch i {
		case 0:
			comment.Slash = orig.Pos()
		case len(lines) - 1:
			comment.Slash = orig.End()
		}
		group.List = append(group.List, comment)
	}
	return group
}

// SplitGunkTag splits '+gunk' tags from a comment group, returning the leading
// documentation and the tags Go expressions.
//
// If pkg is not nil, the tag is also type-checked using the package's type
// information.
func SplitGunkTag(pkg *GunkPackage, fset *token.FileSet, comment *ast.CommentGroup) (string, []loader.GunkTag, error) {
	// Remove the comment leading and / or trailing identifier; // and /* */ and `
	docLines := strings.Split(comment.Text(), "\n")
	var gunkTagLines []string
	var gunkTagPos []int
	var commentLines []string
	foundGunkTag := false
	for i, line := range docLines {
		if strings.HasPrefix(line, "+gunk ") {
			// Replace "+gunk" with spaces, so that we keep the
			// tag's lines all starting at the same column, for
			// accurate position information later.
			gunkTagLine := strings.Replace(line, "+gunk", "     ", 1)
			gunkTagLines = append(gunkTagLines, gunkTagLine)
			gunkTagPos = append(gunkTagPos, i)
			foundGunkTag = true
		} else if foundGunkTag {
			gunkTagLines[len(gunkTagLines)-1] += "\n" + line
		} else {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			commentLines = append(commentLines, line)
		}
	}
	if len(gunkTagLines) == 0 {
		return comment.Text(), nil, nil
	}
	var tags []loader.GunkTag
	for i, gunkTag := range gunkTagLines {
		expr, err := parser.ParseExprFrom(fset, "", gunkTag, 0)
		if err != nil {
			tagPos := fset.Position(comment.Pos())
			tagPos.Line += gunkTagPos[i] // relative to the "+gunk" line
			tagPos.Column += len("// ")  // .Text() stripped these prefixes
			return "", nil, ErrorAbsolutePos(err, tagPos)
		}
		tag := loader.GunkTag{Expr: expr}
		if pkg != nil {
			tv, err := types.Eval(fset, pkg.Types, comment.Pos(), gunkTag)
			if err != nil {
				return "", nil, err
			}
			tag.Type, tag.Value = tv.Type, tv.Value
		}
		tags = append(tags, tag)
	}
	// TODO: make positions in the tag expression absolute too
	return strings.Join(commentLines, "\n"), tags, nil
}

// ErrorAbsolutePos modifies all positions in err, considered to be relative to
// pos. This is useful so that the position information of syntax tree nodes
// parsed from a comment are relative to the entire file, and not only relative
// to the comment containing the source.
func ErrorAbsolutePos(err error, pos token.Position) error {
	list, ok := err.(scanner.ErrorList)
	if !ok {
		return err
	}
	for i, err := range list {
		err.Pos.Filename = pos.Filename
		err.Pos.Line += pos.Line
		err.Pos.Line-- // since these numbers are 1-based
		err.Pos.Column += pos.Column
		err.Pos.Column-- // since these numbers are 1-based
		list[i] = err
	}
	return list
}

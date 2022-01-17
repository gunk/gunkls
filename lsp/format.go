package lsp

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/format"
	"go/printer"
	"go/token"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"github.com/gunk/gunkls/lsp/loader"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
)

func (l *LSP) Format(ctx context.Context, params protocol.DocumentFormattingParams, reply jsonrpc2.Replier) {
	file := params.TextDocument.URI.Filename()
	dir := filepath.Dir(file)
	// if l.config == nil {
	// 	_, err := config.Load(dir)
	// 	if err != nil {
	// 		l.logerr(ctx, "could not load config")
	// 		return
	// 	}
	// }
	// FIXME: use config when PR merged

	// We should be able to assume that the file is already parsed
	// and this is called only on open files with an up to date AST
	pkgs, err := l.loader.Load(dir)
	if err != nil {
		reply(ctx, nil, fmt.Errorf("could not load package: %v", err))
		return
	}
	if len(pkgs) != 1 {
		reply(ctx, nil, fmt.Errorf("expected 1 package, got %d", len(pkgs)))
		return
	}
	pkg := pkgs[0]
	// does this file have errors, or another file?
	if len(pkg.GunkFiles) == 0 {
		l.loader.ParsePackage(pkg, false)
	}
	var fileErr bool
	for _, err := range pkg.Errors {
		if err.File == file {
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
	// format file
	formatted, err := formatFile(l.loader.Fset, f)
	if err != nil {
		reply(ctx, nil, fmt.Errorf("could not format file: %v", err))
		return
	}
	contents := l.loader.InMemoryFiles[file]
	lines := strings.Split(contents, "\n")
	reply(ctx, []protocol.TextEdit{
		{
			Range: protocol.Range{
				Start: protocol.Position{
					Line:      0,
					Character: 0,
				},
				End: protocol.Position{
					Line:      uint32(len(lines) + 1),
					Character: 0,
				},
			},
			NewText: formatted,
		},
	}, nil)
}

func formatFile(fset *token.FileSet, file *ast.File) (_ string, formatErr error) {
	// Use custom panic values to report errors from the inspect func,
	// since that's the easiest way to immediately halt the process and
	// return the error.
	type inspectError struct{ err error }
	defer func() {
		if r := recover(); r != nil {
			if ierr, ok := r.(inspectError); ok {
				formatErr = ierr.err
			} else {
				panic(r)
			}
		}
	}()
	ast.Inspect(file, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.CommentGroup:
			if err := formatComment(fset, node); err != nil {
				panic(inspectError{err})
			}
		case *ast.StructType:
			if err := formatStruct(fset, node); err != nil {
				panic(inspectError{err})
			}
		}
		return true
	})
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, file); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func formatComment(fset *token.FileSet, group *ast.CommentGroup) error {
	// Split the gunk tag ourselves, so we can support Source.
	doc, tags, err := loader.SplitGunkTag(nil, fset, group)
	if err != nil {
		return err
	}
	if len(tags) == 0 {
		// no gunk tags
		return nil
	}
	// If there is leading comments, add a new line
	// between them and the gunk tags.
	if doc != "" {
		doc += "\n\n"
	}
	for i, tag := range tags {
		var buf bytes.Buffer
		// Print with space indentation, since all comment lines begin
		// with "// " and we don't want to mix spaces and tabs.
		config := printer.Config{Mode: printer.UseSpaces, Tabwidth: 8}
		if err := config.Fprint(&buf, fset, tag.Expr); err != nil {
			return err
		}
		doc += "+gunk " + buf.String()
		if i < len(tags)-1 {
			doc += "\n"
		}
	}
	*group = *loader.CommentFromText(group, doc)
	return nil
}

func formatStruct(fset *token.FileSet, st *ast.StructType) error {
	if st.Fields == nil {
		return nil
	}
	// Find which struct fields require sequence numbers, and
	// keep a record of which sequence numbers are already used.
	usedSequences := []int{}
	fieldsWithoutSequence := []*ast.Field{}
	for _, f := range st.Fields.List {
		tag := f.Tag
		if tag == nil {
			fieldsWithoutSequence = append(fieldsWithoutSequence, f)
			continue
		}
		// Can skip the error here because we've already parsed the file.
		str, _ := strconv.Unquote(tag.Value)
		stag := reflect.StructTag(str)
		val, ok := stag.Lookup("pb")
		// If there isn't a 'pb' tag present.
		if !ok {
			fieldsWithoutSequence = append(fieldsWithoutSequence, f)
			continue
		}
		// If there was a 'pb' tag, but it wasn't empty, return an error.
		// It is a bit difficult to add in the sequence number if the 'pb'
		// tag already exists.
		if ok && val == "" {
			errorPos := fset.Position(tag.Pos())
			return fmt.Errorf("%s: struct field tag for pb was empty, please remove or add sequence number", errorPos)
		}
		// If there isn't a number in 'pb' then return an error.
		i, err := strconv.Atoi(val)
		if err != nil {
			errorPos := fset.Position(tag.Pos())
			// TODO: Add the same error checking in generate. Or, look at factoring
			// this code with the code in generate, they do very similar things?
			return fmt.Errorf("%s: struct field tag for pb contains a non-number %q", errorPos, val)
		}
		usedSequences = append(usedSequences, i)
	}
	// Determine missing sequences.
	missingSequences := []int{}
	for i := 1; i < len(st.Fields.List)+1; i++ {
		found := false
		for _, u := range usedSequences {
			if u == i {
				found = true
				break
			}
		}
		if !found {
			missingSequences = append(missingSequences, i)
		}
	}
	// Add the sequence number to the field tag, creating a new
	// tag if one doesn't exist, or prepend the sequence number
	// to the tag that is already there.
	for i, f := range fieldsWithoutSequence {
		nextSequence := missingSequences[i]
		if f.Tag == nil {
			f.Tag = &ast.BasicLit{
				ValuePos: f.Type.End() + 1,
				Kind:     token.STRING,
				Value:    fmt.Sprintf("`pb:\"%d\"`", nextSequence),
			}
		} else {
			// Remove the string quoting around so it is easier to prepend
			// the sequence number.
			tagValueStr, _ := strconv.Unquote(f.Tag.Value)
			f.Tag.Value = fmt.Sprintf("`pb:\"%d\" %s`", nextSequence, tagValueStr)
		}
	}
	return nil
}

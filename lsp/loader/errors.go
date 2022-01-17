package loader

import (
	"fmt"
	"go/scanner"
	"go/token"
	"go/types"
	"log"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"
)

type Error struct {
	File string

	FromLine int
	FromCol  int

	ToLine int
	ToCol  int

	Msg  string
	Kind packages.ErrorKind
}

const (
	UnknownError = packages.UnknownError
	ListError    = packages.ListError
	ParseError   = packages.ParseError
	TypeError    = packages.TypeError
	// Our kinds of errors. Add a gap of 10 to be sure we won't conflict
	// with previous enum values.
	ValidateError = packages.TypeError + 10 + iota
)

func (g *GunkPackage) parseError(file string, err error) {
	// errors.As is intentionally unused to prevent losing context.
	switch v := err.(type) {
	case packages.Error:
		line, col := parsePos(v.Pos)
		g.Errors = append(g.Errors, Error{
			File:     file,
			FromLine: line,
			FromCol:  col,
			ToLine:   line,
			ToCol:    999,
			Msg:      v.Msg,
			Kind:     ParseError,
		})
	case scanner.ErrorList:
		for _, scErr := range v {
			line, col := parsePos(scErr.Pos.String())
			g.Errors = append(g.Errors, Error{
				File:     file,
				FromLine: line,
				FromCol:  col,
				ToLine:   line,
				ToCol:    col,
				Msg:      scErr.Msg,
				Kind:     ParseError,
			})
		}
	default:
		log.Printf("unexpected error: %T: %v", err, err)
	}
}

func (g *GunkPackage) error(file string, from token.Pos, to token.Pos, fset *token.FileSet, msg string, typ packages.ErrorKind) {
	start := fset.Position(from)
	end := fset.Position(to)
	g.Errors = append(g.Errors, Error{
		File:     file,
		FromLine: start.Line - 1,
		FromCol:  start.Column - 1,
		ToLine:   end.Line - 1,
		ToCol:    end.Column - 1,
		Msg:      msg,
		Kind:     typ,
	})
}

func (g *GunkPackage) errorf(kind packages.ErrorKind, tokenPos token.Pos, fset *token.FileSet, format string, args ...interface{}) {
	g.addError(kind, tokenPos, fset, fmt.Errorf(format, args...))
}

func (g *GunkPackage) addError(kind packages.ErrorKind, tokenPos token.Pos, fset *token.FileSet, err error) {
	// Create a packages.Error to add.
	var file string
	var line, col int
	msg := err.Error()
	if tokenPos > 0 && fset != nil {
		pos := fset.Position(tokenPos)
		file, line, col = pos.Filename, pos.Line-1, pos.Column-1
	}
	if typeErr, ok := err.(types.Error); ok {
		// Populate info if the error is a type-checking error from go/types.
		// This prevents an unnecessary -: at the front of error messages.
		pos := typeErr.Fset.Position(typeErr.Pos)
		file, line, col = pos.Filename, pos.Line-1, pos.Column-1
		msg = typeErr.Msg
	}
	g.Errors = append(g.Errors, Error{
		File:     file,
		FromLine: line,
		FromCol:  col,
		ToLine:   line,
		ToCol:    999,
		Msg:      msg,
		Kind:     kind,
	})
}

// parse a pos into the line number and col
//
// a line can be of the following formats:
//	file:line:column    valid position with file name
//	file:line           valid position with file name but no column (column == 0)
//	line:column         valid position without file name
//	line                valid position without file name and no column (column == 0)
//	file                invalid position with file name
//	-                   invalid position without file name
//
// if the position cannot be converted to a valid line number, the line number
// is returned as 0,0
func parsePos(pos string) (line int, col int) {
	defer func() {
		if line < 0 {
			line = 0
		}
		if col < 0 {
			col = 0
		}
	}()

	if pos == "" {
		return 0, 0
	}

	parts := strings.Split(pos, ":")

	if len(parts) == 1 {
		// only  file, or only line
		line, _ := strconv.Atoi(parts[0])
		return line - 1, 0
	}

	if len(parts) == 2 {
		// no column, or line:col
		line, _ := strconv.Atoi(parts[0])
		col, _ := strconv.Atoi(parts[1])
		return line - 1, col - 1
	}

	if len(parts) == 3 {
		// file:line:col
		line, _ := strconv.Atoi(parts[1])
		col, _ := strconv.Atoi(parts[2])
		return line - 1, col - 1
	}

	return 0, 0
}

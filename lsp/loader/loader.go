package loader

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gunk/gunk/loader"
	"go.lsp.dev/protocol"
	"golang.org/x/tools/go/packages"
)

type Loader struct {
	Dir  string
	Fset *token.FileSet
	// If Types is true, we parse and type-check the given packages and all
	// transitive dependencies, including gunk tags. Otherwise, we only
	// parse the given packages.
	Types bool
	cache map[string]*GunkPackage // map from import path to pkg

	stack []string

	// fakeFiles is a list of fake Go files added to make the Go compiler pick
	// up gunk files in packages without Go files.
	fakeFiles map[string][]byte

	// inMemoryFiles is a list of files that are are managed by the language
	// server, that may be in memory. This may not be synced with the contents
	// on disk.
	inMemoryFiles map[string]string
}

// addFakeFile adds a fake Go file to the loader, if needed.
// addFakeFile can return nil even if a file isn't added, if there are not
// any Gunk files in the package, or go files already exist in the package.
func (l *Loader) addFakeFile(pkgName, dirPath string) error {
	infos, err := os.ReadDir(dirPath)
	if err != nil {
		return err
	}
	anyGunk := false
	for _, info := range infos {
		name := info.Name()
		if strings.HasSuffix(name, ".go") {
			// has Go files; nothing to do
			return nil
		}
		if strings.HasSuffix(name, ".gunk") {
			f, err := parser.ParseFile(token.NewFileSet(),
				filepath.Join(dirPath, name), nil, parser.PackageClauseOnly)
			// Ignore errors, since Gunk packages being
			// walked but not being loaded might have
			// invalid syntax.
			if err == nil {
				pkgName = f.Name.Name
			}
			anyGunk = true
			break
		}
	}
	if !anyGunk {
		return nil
	}
	tmpPath := filepath.Join(dirPath, "gunkpkg.go")
	l.fakeFiles[tmpPath] = []byte(`package ` + pkgName)
	return nil
}

// addFakeFiles iterate over all module dependencies of the specified directory
// and adds a fake Go file for all directories inside the dependencies that
// only has Gunk files and no Go files.
// This allows the loader to process Gunk packages using regular Go package
// parsing code when fakeFiles is used as an overlay.
func (l *Loader) addFakeFiles() error {
	l.fakeFiles = make(map[string][]byte)
	// use "." if we encountered an error, for e.g. GOPATH mode
	roots := []string{"."}
	cmd := exec.Command("go", "list", "-m", "-f={{.Dir}}", "all")
	cmd.Dir = l.Dir
	if out, err := cmd.Output(); err == nil {
		rootOutput := strings.Split(strings.TrimSpace(string(out)), "\n")
		roots = make([]string, 0, len(rootOutput))
		for _, v := range rootOutput {
			roots = append(roots, strings.TrimSpace(v))
		}
	}
	// Walk through all directories and add fake files for all packages that
	// only have gunk files.
	for _, root := range roots {
		if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.IsDir() {
				return nil
			}
			return l.addFakeFile(info.Name(), path)
		}); err != nil {
			return err
		}
	}
	return nil
}

// Loader finds all of the gunk files in path.
// Cached files are not loaded again.
// No type checking or parsing is done.
func (l *Loader) Load(path string) ([]*GunkPackage, error) {
	if l.cache == nil {
		l.cache = make(map[string]*GunkPackage)
	}
	// use cache, if exists
	if pkg := l.cache[path]; pkg != nil {
		if len(pkg.Package.Errors) > 0 {
			return nil, fmt.Errorf("error loading package %q", path)
		}
		return []*GunkPackage{pkg}, nil
	}
	// Generate fake files if it has not been initialized yet.
	if l.fakeFiles == nil {
		err := l.addFakeFiles()
		if err != nil {
			return nil, err
		}
	}
	// Load the Gunk packages as Go packages.
	var pkgs []*GunkPackage
	cfg := &packages.Config{
		Dir:     l.Dir,
		Mode:    packages.NeedName | packages.NeedFiles,
		Overlay: l.fakeFiles,
	}
	lpkgs, err := packages.Load(cfg, path)
	if err != nil {
		return nil, err
	}
	for _, lpkg := range lpkgs {
		pkg := NewGunkPackage(*lpkg, Untracked)
		findGunkFiles(pkg)
		if len(pkg.GunkFiles) == 0 && len(lpkg.Errors) == 0 {
			// Not a Gunk package. Skip.
			continue
		}
		pkgs = append(pkgs, pkg)
	}
	// Add the Gunk files to each package.
	for _, pkg := range pkgs {
		l.cache[pkg.PkgPath] = pkg
	}
	return pkgs, nil
}

// AddFile adds a gunk file to the gunk package, and removes all cached entries
// and imports that directly or indirectly import the package of the file.
func (l *Loader) AddFile(pkgs []*GunkPackage, path, src string) ([]*GunkPackage, *GunkPackage, error) {
	if l.inMemoryFiles == nil {
		l.inMemoryFiles = make(map[string]string)
	}
	l.inMemoryFiles[path] = src
	// Find the package that contains the file.
	var pkg *GunkPackage
	dir := filepath.Dir(path)
	for _, p := range pkgs {
		if dir == p.Dir {
			if p.State == Untracked {
				p.State = Dirty
			}
			pkg = p
			break
		}
	}
	// It's a new package, we can assume nothing imports it.
	if pkg == nil {
		pkgName := filepath.Base(dir)
		f, err := parser.ParseFile(token.NewFileSet(), path, src, parser.PackageClauseOnly)
		// Ignore errors, since Gunk packages being
		// walked but not being loaded might have
		// invalid syntax.
		if err == nil {
			pkgName = f.Name.Name
		}
		// Add fake file to the loader, if needed.
		infos, err := os.ReadDir(dir)
		switch {
		case errors.Is(err, os.ErrNotExist):
			// The directory does not yet exist, it is just an in memory buffer
			// that will be written to disk later.
			//
			// FIXME: this does not actually work. Creating a buffer
			// for a file that does not have a directory will not load
			// that file.
			tmpPath := filepath.Join(dir, "gunkpkg.go")
			l.fakeFiles[tmpPath] = []byte(`package ` + pkgName)
		case err != nil:
			return pkgs, nil, err
		default:
			addFake := true
			for _, info := range infos {
				if strings.HasSuffix(info.Name(), ".go") {
					addFake = false
					break
				}
			}
			if addFake {
				tmpPath := filepath.Join(dir, "gunkpkg.go")
				l.fakeFiles[tmpPath] = []byte(`package ` + pkgName)
			}
		}
		// Add new package.
		cfg := &packages.Config{
			Dir:     dir,
			Mode:    packages.NeedName | packages.NeedFiles,
			Overlay: l.fakeFiles,
		}
		lpkgs, err := packages.Load(cfg, path)
		if err != nil {
			return pkgs, nil, err // errors here
		}
		if len(lpkgs) != 1 {
			return pkgs, nil, fmt.Errorf("unexpected number of packages: %d", len(lpkgs))
		}
		pkg := NewGunkPackage(*lpkgs[0], Dirty)
		findGunkFiles(pkg)
	}
	var exists bool
	for _, file := range pkg.GunkFiles {
		if file == path {
			exists = true
			break
		}
	}
	// The Gunk file is currently only in memory.
	if !exists {
		pkg.GunkFiles = append(pkg.GunkFiles, path)
	}
	// Add the file to the package.
	// Remove all cached entries and imports that directly or indirectly
	// import the package of the file.
	delete(l.cache, pkg.PkgPath)
	for _, p := range pkgs {
		for pkgPath := range p.Imports {
			if pkgPath == pkg.PkgPath {
				p.Imports[pkgPath] = pkg.GunkPackage
				// Mark as dirty, resend diagnostics for open packages that
				// import this package.
				if p.State == Open {
					p.State = Dirty
				}
			}
		}
	}
	return pkgs, pkg, nil
}

func (l *Loader) UpdateFile(pkgs []*GunkPackage, path, src string) ([]*GunkPackage, error) {
	if l.inMemoryFiles == nil {
		l.inMemoryFiles = make(map[string]string)
	}
	l.inMemoryFiles[path] = src
	// Find the package that contains the file.
	var pkg *GunkPackage
	dir := filepath.Dir(path)
	for _, p := range pkgs {
		if dir == p.Dir {
			p.State = Dirty
			pkg = p
			break
		}
	}
	if pkg == nil {
		// unlock to call addFile
		var err error
		pkgs, pkg, err = l.AddFile(pkgs, path, src)
		if err != nil {
			return pkgs, err
		}
	}
	findGunkFiles(pkg)
	// Add the file to the package.
	var exists bool
	for _, file := range pkg.GunkFiles {
		if file == path {
			exists = true
			break
		}
	}
	// The Gunk file is currently only in memory.
	if !exists {
		pkg.GunkFiles = append(pkg.GunkFiles, path)
	}
	// Remove all cached entries and imports that directly or indirectly
	// import the package of the file.
	delete(l.cache, pkg.PkgPath)
	for _, p := range pkgs {
		for pkgPath := range p.Imports {
			if pkgPath == pkg.PkgPath {
				// Mark as dirty, resend diagnostics for open packages that
				// import this package.
				if p.State == Open {
					p.State = Dirty
				}
			}
		}
	}
	return pkgs, nil
}

func (l *Loader) CloseFile(pkgs []*GunkPackage, path string) ([]*GunkPackage, error) {
	delete(l.inMemoryFiles, path)
	// Find the package that contains the file.
	var pkg *GunkPackage
	var index int

	dir := filepath.Dir(path)
	for i, p := range pkgs {
		if dir == p.Dir {
			p.State = Dirty
			pkg = p
			index = i
			break
		}
	}
	if pkg == nil {
		return pkgs, fmt.Errorf("could not find loaded package to close")
	}
	resetPackage(pkg)
	findGunkFiles(pkg)
	if len(pkg.GunkFiles) == 0 {
		pkgs = append(pkgs[:index], pkgs[index+1:]...)
	}
	delete(l.cache, pkg.PkgPath)
	for _, p := range pkgs {
		for pkgPath := range p.Imports {
			if pkgPath == pkg.PkgPath {
				// Mark as dirty, resend diagnostics for open packages that
				// import this package.
				if p.State == Open {
					p.State = Dirty
				}
			}
		}
	}
	return pkgs, nil
}

// findGunkFiles fills a package's GunkFiles field with the gunk files found in
// the package directory. This is used when loading a Gunk package via an import
// path or a directory.
//
// Note that this requires all the source files within the package to be in the
// same directory, which is true for Go Modules and GOPATH, but not other build
// systems like Bazel.
func findGunkFiles(pkg *GunkPackage) {
	for _, gofile := range pkg.GoFiles {
		dir := filepath.Dir(gofile)
		if pkg.Dir == "" {
			pkg.Dir = dir
		} else if dir != pkg.Dir {
			pkg.errorf(ListError, 0, nil, "multiple dirs for %s: %s %s",
				pkg.PkgPath, pkg.Dir, dir)
			return // we can't continue
		}
	}
	matches, err := filepath.Glob(filepath.Join(pkg.Dir, "*.gunk"))
	if err != nil {
		// can only be a malformed pattern; should never happen.
		panic(err.Error())
	}
	pkg.GunkFiles = matches
}

func (l *Loader) Errors(pkgs []*GunkPackage, pkg *GunkPackage) (map[string][]protocol.Diagnostic, error) {
	// If the package is not dirty, return the cached diagnostics.
	if pkg.State != Dirty {
		return nil, nil
	}

	resetPackage(pkg)
	// Populate gunk package contents
	l.parseGunkPackage(pkg)
	l.validatePackage(pkg)

	diagnostics := make(map[string][]protocol.Diagnostic)
	for _, f := range pkg.GunkFiles {
		diagnostics[f] = nil
	}

	for _, pErr := range pkg.Errors {

		code := "error"
		switch pErr.Kind {
		case UnknownError:
			code = "error"
		case ParseError:
			code = "parse error"
		case TypeError:
			code = "type error"
		case ValidateError:
			code = "validate error"
		}

		d := protocol.Diagnostic{
			Range: protocol.Range{
				Start: protocol.Position{
					Line:      uint32(pErr.FromLine),
					Character: uint32(pErr.FromCol),
				},
				End: protocol.Position{
					Line:      uint32(pErr.ToLine),
					Character: uint32(pErr.ToCol),
				},
			},
			Code:     code,
			Severity: 1,
			Source:   "coc-gunk",
			Message:  pErr.Msg,
		}
		diagnostics[pErr.File] = append(diagnostics[pErr.File], d)
	}

	return diagnostics, nil
}

// Import satisfies the go/types.Importer interface.
//
// Unlike standard Go ones like go/importer and x/tools/go/packages, this one is
// adapted to load Gunk packages.
//
// Aside from that, it is very similar to standard Go importers that load from
// source.
func (l *Loader) Import(path string) (*types.Package, error) {
	if !strings.Contains(path, ".") {
		cfg := &packages.Config{Mode: packages.LoadTypes}
		pkgs, err := packages.Load(cfg, path)
		if err != nil {
			return nil, err
		}
		if len(pkgs) != 1 {
			panic("expected go/packages.Load to return exactly one package")
		}
		return pkgs[0].Types, nil
	}
	pkgs, err := l.Load(path)
	if err != nil {
		return nil, err
	}
	if len(pkgs) != 1 {
		panic("expected Loader.Load to return exactly one package")
	}
	pkg := pkgs[0]
	if len(pkg.Package.Errors) != 0 {
		// slightly crude, but we don't have a better way test the error
		return nil, fmt.Errorf(pkg.Package.Errors[0].Msg)
	}
	if pkg.State == Dirty || pkg.Types == nil {
		resetPackage(pkg)
		l.parseGunkPackage(pkg)
	}
	return pkgs[0].Types, nil
}

type PackageState int

const (
	Untracked PackageState = iota
	Dirty
	Building
	Open
)

type GunkPackage struct {
	*loader.GunkPackage

	Errors []Error

	State PackageState
}

func NewGunkPackage(pkg packages.Package, state PackageState) *GunkPackage {
	return &GunkPackage{
		GunkPackage: &loader.GunkPackage{
			Package: pkg,
		},
		State: state,
	}
}

func resetPackage(pkg *GunkPackage) {
	pkg.GunkNames = nil
	pkg.GunkSyntax = nil
	pkg.ProtoName = ""
	pkg.Errors = nil
	pkg.Types = nil
	pkg.Package = packages.Package{
		ID:      pkg.Package.ID,
		Name:    pkg.Package.Name,
		PkgPath: pkg.Package.PkgPath,
		GoFiles: pkg.Package.GoFiles,
	}
}

func getNode(pos token.Pos, file *ast.File) ast.Node {
	var candidates []ast.Node
	ast.Inspect(file, func(n ast.Node) bool {
		if n != nil && n.Pos() == pos {
			candidates = append(candidates, n)
		}
		return true
	})
	// get shortest node that finishes
	var shortest ast.Node
	for _, candidate := range candidates {
		if shortest == nil {
			shortest = candidate
			continue
		}
		if candidate.End() < shortest.End() {
			shortest = candidate
		}
	}
	return shortest
}

package lsp

import (
	"context"
	"fmt"
	"go/token"
	"log"
	"net/url"
	"path/filepath"

	"github.com/gunk/gls/lsp/loader"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

func (l *LSP) Load(ctx context.Context) error {
	workspace, err := url.ParseRequestURI(l.workspace.URI)
	if err != nil {
		return fmt.Errorf("could not load workspace: %w", err)
	}

	l.loader = &loader.Loader{
		Dir:   workspace.Path,
		Fset:  token.NewFileSet(),
		Types: false,
	}

	l.pkgs, err = l.loader.Load(workspace.Path + "/...")
	if err != nil {
		return err
	}

	return nil
}

func (l *LSP) OpenFile(ctx context.Context, data protocol.DidOpenTextDocumentParams) error {
	path := data.TextDocument.URI.Filename()
	// Add to pkgs
	var err error
	l.pkgs, _, err = l.loader.AddFile(l.pkgs, path, data.TextDocument.Text)
	if err != nil {
		log.Println("error adding new file:", err)
	}

	for _, pkg := range l.pkgs {
		if pkg.State != loader.Dirty {
			continue
		}
		diags, err := l.loader.Errors(l.pkgs, pkg)
		if err != nil {
			log.Printf("could not load diagnostics: %v", err)
		}

		for file, d := range diags {
			l.conn.Notify(ctx, protocol.MethodTextDocumentPublishDiagnostics, protocol.PublishDiagnosticsParams{
				URI:         uri.File(file),
				Diagnostics: d,
			})
		}
	}

	return err
}

func (l *LSP) UpdateFile(ctx context.Context, data protocol.DidChangeTextDocumentParams) error {
	path := data.TextDocument.URI.Filename()
	// Add to pkgs
	var err error
	l.pkgs, err = l.loader.UpdateFile(l.pkgs, path, data.ContentChanges[0].Text)
	if err != nil {
		log.Println("error adding new file:", err)
	}

	for _, pkg := range l.pkgs {
		if pkg.State != loader.Dirty {
			continue
		}
		diags, err := l.loader.Errors(l.pkgs, pkg)
		if err != nil {
			log.Printf("could not load diagnostics: %v", err)
		}

		for file, d := range diags {
			if filepath.Dir(file) != pkg.Dir {
				log.Println("skipping sending", len(d), "diagnostics for", file)
			}
			if len(d) != 0 {
				log.Println("sending", len(d), "for", file)
			}
			l.conn.Notify(ctx, protocol.MethodTextDocumentPublishDiagnostics, protocol.PublishDiagnosticsParams{
				URI:         uri.File(file),
				Diagnostics: d,
			})
		}
	}

	return err
}

func (l *LSP) CloseFile(ctx context.Context, data protocol.DidCloseTextDocumentParams) error {
	path := data.TextDocument.URI.Filename()
	var err error
	l.pkgs, err = l.loader.CloseFile(l.pkgs, path)
	if err != nil {
		log.Println("error adding closing file:", err)
	}
	return nil
}

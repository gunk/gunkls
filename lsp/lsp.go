package lsp

import (
	"context"
	"encoding/json"
	"log"
	"sync"

	"github.com/gunk/gls/lsp/loader"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
)

type LSP struct {
	mu sync.Mutex

	conn jsonrpc2.Conn

	initialized bool
	version     string

	loader    *loader.Loader
	workspace protocol.WorkspaceFolder
	pkgs      []*loader.GunkPackage
}

func NewLSPServer(version string, conn jsonrpc2.Conn) *LSP {
	return &LSP{
		conn:        conn,
		initialized: false,
	}
}

func (l *LSP) Handle(ctx context.Context, reply jsonrpc2.Replier, r jsonrpc2.Request) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	log.Printf("Requested '%s'\n", r.Method())

	switch r.Method() {
	case protocol.MethodInitialize:
		if l.initialized {
			return nil
		}
		l.initialized = true
		var params protocol.InitializeParams
		if err := json.Unmarshal(r.Params(), &params); err != nil {
			return err
		}
		if len(params.WorkspaceFolders) == 0 {
			l.msg(ctx, protocol.MessageTypeError, "No workspace folders found!")
			return nil
		}

		err := reply(ctx, protocol.InitializeResult{
			Capabilities: protocol.ServerCapabilities{
				TextDocumentSync: protocol.TextDocumentSyncOptions{
					OpenClose: true,
					Change:    protocol.TextDocumentSyncKindFull,
				},

				CompletionProvider: &protocol.CompletionOptions{
					ResolveProvider: true,
				},
			},
			ServerInfo: &protocol.ServerInfo{
				Name:    "gls",
				Version: l.version,
			},
		}, nil)

		l.workspace = params.WorkspaceFolders[0]
		// load gunk
		if err := l.Load(ctx); err != nil {
			l.logerr(ctx, "Could not load: "+err.Error())
		} else {
			l.msg(ctx, protocol.MessageTypeInfo, "Loaded workspace "+l.workspace.Name)
		}
		return err

	case protocol.MethodInitialized:
		return nil
	case protocol.MethodTextDocumentDidOpen:
		var params protocol.DidOpenTextDocumentParams
		if err := json.Unmarshal(r.Params(), &params); err != nil {
			return err
		}
		l.OpenFile(ctx, params)
		return nil
	case protocol.MethodTextDocumentDidChange:
		var params protocol.DidChangeTextDocumentParams
		if err := json.Unmarshal(r.Params(), &params); err != nil {
			return err
		}
		l.UpdateFile(ctx, params)
		return nil

	}
	return nil
}

func (l *LSP) log(ctx context.Context, msg string) {
	l.conn.Notify(ctx, protocol.MethodWindowLogMessage, protocol.LogMessageParams{
		Type:    protocol.MessageTypeInfo,
		Message: msg,
	})
}

func (l *LSP) logerr(ctx context.Context, msg string) {
	l.conn.Notify(ctx, protocol.MethodWindowLogMessage, protocol.LogMessageParams{
		Type:    protocol.MessageTypeError,
		Message: msg,
	})
}

func (l *LSP) msg(ctx context.Context, typ protocol.MessageType, msg string) {
	l.conn.Notify(ctx, protocol.MethodWindowShowMessage, protocol.ShowMessageParams{
		Type:    typ,
		Message: msg,
	})
}

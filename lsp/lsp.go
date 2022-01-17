package lsp

import (
	"context"
	"encoding/json"
	"log"
	"sync"

	"github.com/gunk/gunk/config"
	"github.com/gunk/gunkls/lsp/loader"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
)

type LSP struct {
	mu sync.Mutex

	conn jsonrpc2.Conn

	initialized bool
	version     string
	lint        bool

	loader    *loader.Loader
	workspace protocol.WorkspaceFolder
	pkgs      []*loader.GunkPackage
	config    *config.Config
}

type Config struct {
	Version string
	Lint    bool

	Conn jsonrpc2.Conn
}

func NewLSPServer(config Config) *LSP {
	return &LSP{
		version: config.Version,
		lint:    config.Lint,
		conn:    config.Conn,
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
					ResolveProvider: false,
				},
				DocumentFormattingProvider: true,
				// DefinitionProvider:         true,
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
	// Text Synchronization
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
	case protocol.MethodTextDocumentDidClose:
		var params protocol.DidCloseTextDocumentParams
		if err := json.Unmarshal(r.Params(), &params); err != nil {
			return err
		}
		l.CloseFile(ctx, params)
		return nil
	case protocol.MethodTextDocumentFormatting:
		var params protocol.DocumentFormattingParams
		if err := json.Unmarshal(r.Params(), &params); err != nil {
			return err
		}
		l.Format(ctx, params, reply)
		return nil
	// Language Server Specific Features
	case protocol.MethodTextDocumentDefinition:
		var params protocol.TextDocumentPositionParams
		if err := json.Unmarshal(r.Params(), &params); err != nil {
			return err
		}
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

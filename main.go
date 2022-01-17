package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"

	"github.com/gunk/gunkls/lsp"

	"go.lsp.dev/jsonrpc2"
)

const version = "0.0.1"

var (
	pprofPort = flag.Int("pprof", -1, "enables pprof on the specified port")
	lint      = flag.Bool("lint", false, "run linter")
)

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	flag.Parse()

	if *pprofPort > 0 {
		log.Println("starting pprof on port", *pprofPort)
		go func() {
			http.ListenAndServe(fmt.Sprintf("127.0.0.1:%d", *pprofPort), nil)
		}()
	}
	log.Println("gunkls: reading on stdin, writing on stdout")
	if *lint {
		log.Println("gunkls: linting enabled")
	}

	stream := jsonrpc2.NewStream(stdrwc{})
	conn := jsonrpc2.NewConn(stream)

	config := lsp.Config{
		Lint:    *lint,
		Version: version,
		Conn:    conn,
	}
	server := jsonrpc2.HandlerServer(lsp.NewLSPServer(config).Handle)
	return server.ServeStream(ctx, conn)
}

type stdrwc struct{}

func (stdrwc) Read(p []byte) (int, error) {
	return os.Stdin.Read(p)
}

func (stdrwc) Write(p []byte) (int, error) {
	return os.Stdout.Write(p)
}

func (stdrwc) Close() error {
	if err := os.Stdin.Close(); err != nil {
		return err
	}
	return os.Stdout.Close()
}

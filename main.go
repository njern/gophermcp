package main

import (
	"log"

	mcp "github.com/metoro-io/mcp-golang"
	"github.com/metoro-io/mcp-golang/transport/stdio"
	"github.com/njern/gophermcp/godoc"
)

func main() {
	done := make(chan struct{})

	server := mcp.NewServer(stdio.NewStdioServerTransport(), mcp.WithName("gophermcp"), mcp.WithVersion("v1.0.0"))

	defer godoc.Cleanup()
	err := server.RegisterTool("godoc", godoc.ToolDescription, godoc.Tool)
	if err != nil {
		log.Panicf("failed to register 'godoc' tool: %v", err)
	}

	err = server.Serve()
	if err != nil {
		log.Panicf("failed to start server: %v", err)
	}

	<-done
}

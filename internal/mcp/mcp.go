// Package mcp exposes the engram engine as MCP tools. It's a thin adapter:
// every handler just calls the shared core.Engine, so MCP and REST behave
// identically. The server is served over streamable HTTP (and can also run
// over stdio).
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/davasorus/engram/internal/core"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Adapter wires a core.Engine to an MCP server.
type Adapter struct {
	eng    *core.Engine
	server *mcp.Server
}

func New(eng *core.Engine) *Adapter {
	s := mcp.NewServer(&mcp.Implementation{Name: "engram", Version: "0.1.0"}, nil)
	a := &Adapter{eng: eng, server: s}
	a.registerTools()
	return a
}

// Server exposes the underlying MCP server (for stdio runs).
func (a *Adapter) Server() *mcp.Server { return a.server }

// HTTPHandler returns a streamable-HTTP handler serving this MCP server.
func (a *Adapter) HTTPHandler() http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return a.server }, nil)
}

// --- tool input types (the SDK derives JSON schemas from these) ------------

type searchIn struct {
	Query string `json:"query" jsonschema:"the natural-language search query"`
	Limit int    `json:"limit,omitempty" jsonschema:"max results (default 10)"`
	Kind  string `json:"kind,omitempty" jsonschema:"'semantic' (default) or 'keyword'"`
}
type readIn struct {
	ID string `json:"id" jsonschema:"the note id"`
}
type writeIn struct {
	ID    string   `json:"id,omitempty" jsonschema:"note id; derived from title if omitted"`
	Title string   `json:"title" jsonschema:"note title (required)"`
	Body  string   `json:"body" jsonschema:"markdown body"`
	Tags  []string `json:"tags,omitempty" jsonschema:"optional tags"`
}
type patchIn struct {
	ID     string `json:"id" jsonschema:"note id"`
	OldStr string `json:"old_str" jsonschema:"exact text to replace (must be unique)"`
	NewStr string `json:"new_str" jsonschema:"replacement text"`
}
type linksIn struct {
	ID string `json:"id" jsonschema:"note id or title to find backlinks for"`
}
type listIn struct {
	Limit  int `json:"limit,omitempty"`
	Offset int `json:"offset,omitempty"`
}
type deleteIn struct {
	ID string `json:"id" jsonschema:"note id to delete"`
}

func (a *Adapter) registerTools() {
	mcp.AddTool(a.server, &mcp.Tool{
		Name:        "mem_search",
		Description: "Search the agent's memory by meaning (semantic) or keyword. Returns ranked notes with scores.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, any, error) {
		hits, err := a.eng.Search(ctx, in.Query, in.Limit, in.Kind)
		if err != nil {
			return errResult(err), nil, nil
		}
		return jsonResult(hits), nil, nil
	})

	mcp.AddTool(a.server, &mcp.Tool{
		Name:        "mem_read",
		Description: "Read a note from memory by id, including its markdown body and outgoing links.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in readIn) (*mcp.CallToolResult, any, error) {
		n, err := a.eng.Read(ctx, in.ID)
		if err != nil {
			return errResult(err), nil, nil
		}
		if n == nil {
			return errResult(fmt.Errorf("note %q not found", in.ID)), nil, nil
		}
		return jsonResult(n), nil, nil
	})

	mcp.AddTool(a.server, &mcp.Tool{
		Name:        "mem_write",
		Description: "Create or update a memory note. The body is markdown; [[wikilinks]] are parsed into links, and the note is embedded for semantic search.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in writeIn) (*mcp.CallToolResult, any, error) {
		n, err := a.eng.Write(ctx, core.WriteInput{ID: in.ID, Title: in.Title, Body: in.Body, Tags: in.Tags})
		if err != nil {
			return errResult(err), nil, nil
		}
		return jsonResult(n), nil, nil
	})

	mcp.AddTool(a.server, &mcp.Tool{
		Name:        "mem_patch",
		Description: "Edit an existing note by replacing an exact, unique substring. Re-embeds and re-links automatically.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in patchIn) (*mcp.CallToolResult, any, error) {
		n, err := a.eng.Patch(ctx, in.ID, in.OldStr, in.NewStr)
		if err != nil {
			return errResult(err), nil, nil
		}
		return jsonResult(n), nil, nil
	})

	mcp.AddTool(a.server, &mcp.Tool{
		Name:        "mem_links",
		Description: "List backlinks: notes that link to the given note id or title.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in linksIn) (*mcp.CallToolResult, any, error) {
		bl, err := a.eng.Backlinks(ctx, in.ID)
		if err != nil {
			return errResult(err), nil, nil
		}
		return jsonResult(bl), nil, nil
	})

	mcp.AddTool(a.server, &mcp.Tool{
		Name:        "mem_list",
		Description: "List notes, most-recently-updated first.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, any, error) {
		ns, err := a.eng.List(ctx, in.Limit, in.Offset)
		if err != nil {
			return errResult(err), nil, nil
		}
		return jsonResult(ns), nil, nil
	})

	mcp.AddTool(a.server, &mcp.Tool{
		Name:        "mem_delete",
		Description: "Delete a note by id.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in deleteIn) (*mcp.CallToolResult, any, error) {
		if err := a.eng.Delete(ctx, in.ID); err != nil {
			return errResult(err), nil, nil
		}
		return jsonResult(map[string]string{"deleted": in.ID}), nil, nil
	})
}

// --- result helpers ---------------------------------------------------------

func jsonResult(v any) *mcp.CallToolResult {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return errResult(err)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}
}

func errResult(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: "ERROR: " + err.Error()}},
	}
}

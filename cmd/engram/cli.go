package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/davasorus/engram/internal/cliclient"
	"github.com/davasorus/engram/internal/core"
)

// cliCommands lists the subcommands runCLI understands. main() checks the
// first argument against this set before falling through to server
// startup, so "engram search foo" and "engram -addr :9000" both work: a
// bare flag never collides with a subcommand name.
var cliCommands = map[string]func(ctx context.Context, out io.Writer, args []string) int{
	"health":  cliHealth,
	"search":  cliSearch,
	"list":    cliList,
	"get":     cliGet,
	"write":   cliWrite,
	"patch":   cliPatch,
	"delete":  cliDelete,
	"links":   cliLinks,
	"suggest": cliSuggest,
	"summary": cliSummary,
	"reembed": cliReembed,
	"help":    cliHelp,
	"--help":  cliHelp,
	"-h":      cliHelp,
}

// isCLICommand reports whether name is one of the recognized subcommands.
func isCLICommand(name string) bool {
	_, ok := cliCommands[name]
	return ok
}

// runCLI dispatches a CLI subcommand and returns the process exit code. It
// is a thin wrapper over internal/cliclient: every subcommand issues one
// REST call to a running engram server and prints the result, so a human
// can manage or debug notes from a terminal without the web UI or curl.
func runCLI(ctx context.Context, out io.Writer, args []string) int {
	if len(args) == 0 {
		return cliHelp(ctx, out, args)
	}
	fn, ok := cliCommands[args[0]]
	if !ok {
		_, _ = fmt.Fprintf(out, "unknown command %q; run \"engram help\" for a list\n", args[0])
		return 1
	}
	return fn(ctx, out, args[1:])
}

// cliServerFlag adds the shared -server flag to a subcommand's FlagSet and
// returns a func to read its resolved value (flag wins, then
// ENGRAM_CLI_SERVER, then a localhost default).
func cliServerFlag(fs *flag.FlagSet) func() string {
	def := env("ENGRAM_CLI_SERVER", "http://localhost:8088")
	v := fs.String("server", def, "engram server base URL")
	return func() string { return *v }
}

func cliPrintJSON(out io.Writer, v any) int {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		_, _ = fmt.Fprintf(out, "error: %v\n", err)
		return 1
	}
	return 0
}

func cliFail(out io.Writer, err error) int {
	_, _ = fmt.Fprintf(out, "error: %v\n", err)
	return 1
}

func cliHealth(ctx context.Context, out io.Writer, args []string) int {
	fs := flag.NewFlagSet("health", flag.ContinueOnError)
	server := cliServerFlag(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	h, err := cliclient.New(server()).Health(ctx)
	if err != nil {
		return cliFail(out, err)
	}
	return cliPrintJSON(out, h)
}

func cliSearch(ctx context.Context, out io.Writer, args []string) int {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	server := cliServerFlag(fs)
	project := fs.String("project", "", "restrict to this project")
	limit := fs.Int("limit", 0, "max results (0 = server default)")
	kind := fs.String("kind", "", "semantic (default) | keyword | hybrid")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	q := strings.Join(fs.Args(), " ")
	if q == "" {
		_, _ = fmt.Fprintln(out, "usage: engram search [-project P] [-limit N] [-kind semantic|keyword|hybrid] <query>")
		return 2
	}
	hits, err := cliclient.New(server()).Search(ctx, *project, q, *limit, *kind)
	if err != nil {
		return cliFail(out, err)
	}
	return cliPrintJSON(out, hits)
}

func cliList(ctx context.Context, out io.Writer, args []string) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	server := cliServerFlag(fs)
	project := fs.String("project", "", "restrict to this project")
	limit := fs.Int("limit", 0, "max results (0 = server default)")
	offset := fs.Int("offset", 0, "pagination offset")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ns, err := cliclient.New(server()).List(ctx, *project, *limit, *offset)
	if err != nil {
		return cliFail(out, err)
	}
	return cliPrintJSON(out, ns)
}

func cliGet(ctx context.Context, out io.Writer, args []string) int {
	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	server := cliServerFlag(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		_, _ = fmt.Fprintln(out, "usage: engram get <id>")
		return 2
	}
	n, err := cliclient.New(server()).Get(ctx, fs.Arg(0))
	if err != nil {
		return cliFail(out, err)
	}
	return cliPrintJSON(out, n)
}

func cliWrite(ctx context.Context, out io.Writer, args []string) int {
	fs := flag.NewFlagSet("write", flag.ContinueOnError)
	server := cliServerFlag(fs)
	id := fs.String("id", "", "note id (optional; derived from title if empty)")
	project := fs.String("project", "", "project scope")
	title := fs.String("title", "", "note title (required)")
	tags := fs.String("tags", "", "comma-separated tags")
	bodyFile := fs.String("body-file", "", "read the note body from this file (- for stdin)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *title == "" {
		_, _ = fmt.Fprintln(out, "usage: engram write -title T [-id ID] [-project P] [-tags a,b] [-body-file F|-]")
		return 2
	}
	// An empty -body-file writes an empty body (e.g. for a title-only
	// placeholder note); "-" reads from stdin, anything else is a path.
	var body []byte
	var err error
	switch *bodyFile {
	case "":
	case "-":
		body, err = io.ReadAll(os.Stdin)
	default:
		body, err = os.ReadFile(*bodyFile)
	}
	if err != nil {
		return cliFail(out, err)
	}
	var tagList []string
	if *tags != "" {
		tagList = strings.Split(*tags, ",")
	}
	n, err := cliclient.New(server()).Write(ctx, core.WriteInput{
		ID: *id, Project: *project, Title: *title, Body: string(body), Tags: tagList,
	})
	if err != nil {
		return cliFail(out, err)
	}
	return cliPrintJSON(out, n)
}

func cliPatch(ctx context.Context, out io.Writer, args []string) int {
	fs := flag.NewFlagSet("patch", flag.ContinueOnError)
	server := cliServerFlag(fs)
	oldStr := fs.String("old", "", "exact text to replace (required)")
	newStr := fs.String("new", "", "replacement text")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 || *oldStr == "" {
		_, _ = fmt.Fprintln(out, "usage: engram patch -old TEXT [-new TEXT] <id>")
		return 2
	}
	n, err := cliclient.New(server()).Patch(ctx, fs.Arg(0), *oldStr, *newStr)
	if err != nil {
		return cliFail(out, err)
	}
	return cliPrintJSON(out, n)
}

func cliDelete(ctx context.Context, out io.Writer, args []string) int {
	fs := flag.NewFlagSet("delete", flag.ContinueOnError)
	server := cliServerFlag(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		_, _ = fmt.Fprintln(out, "usage: engram delete <id>")
		return 2
	}
	if err := cliclient.New(server()).Delete(ctx, fs.Arg(0)); err != nil {
		return cliFail(out, err)
	}
	_, _ = fmt.Fprintf(out, "deleted %s\n", fs.Arg(0))
	return 0
}

func cliLinks(ctx context.Context, out io.Writer, args []string) int {
	fs := flag.NewFlagSet("links", flag.ContinueOnError)
	server := cliServerFlag(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		_, _ = fmt.Fprintln(out, "usage: engram links <id>")
		return 2
	}
	bl, err := cliclient.New(server()).Links(ctx, fs.Arg(0))
	if err != nil {
		return cliFail(out, err)
	}
	return cliPrintJSON(out, bl)
}

func cliSuggest(ctx context.Context, out io.Writer, args []string) int {
	fs := flag.NewFlagSet("suggest", flag.ContinueOnError)
	server := cliServerFlag(fs)
	limit := fs.Int("limit", 0, "max results (0 = server default)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		_, _ = fmt.Fprintln(out, "usage: engram suggest [-limit N] <id>")
		return 2
	}
	hits, err := cliclient.New(server()).Suggestions(ctx, fs.Arg(0), *limit)
	if err != nil {
		return cliFail(out, err)
	}
	return cliPrintJSON(out, hits)
}

func cliSummary(ctx context.Context, out io.Writer, args []string) int {
	fs := flag.NewFlagSet("summary", flag.ContinueOnError)
	server := cliServerFlag(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		_, _ = fmt.Fprintln(out, "usage: engram summary <id>")
		return 2
	}
	s, err := cliclient.New(server()).Summary(ctx, fs.Arg(0))
	if err != nil {
		return cliFail(out, err)
	}
	_, _ = fmt.Fprintln(out, s)
	return 0
}

func cliReembed(ctx context.Context, out io.Writer, args []string) int {
	fs := flag.NewFlagSet("reembed", flag.ContinueOnError)
	server := cliServerFlag(fs)
	full := fs.Bool("full", false, "rebuild every note's vector, not just notes missing one")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	n, err := cliclient.New(server()).Reembed(ctx, *full)
	if err != nil {
		return cliFail(out, err)
	}
	_, _ = fmt.Fprintf(out, "reembedded %d notes\n", n)
	return 0
}

const cliHelpText = `engram: memory service for AI agents

Run with no subcommand (or with server flags like -dsn/-addr) to start the
service. Run with one of these subcommands to act as a client against a
running server (default http://localhost:8088, override with -server or
ENGRAM_CLI_SERVER):

  health                          check server status
  search [opts] <query>           semantic/keyword/hybrid search
  list [opts]                     list notes, most recently updated first
  get <id>                        fetch one note
  write -title T [opts]           create or update a note
  patch -old T [-new T] <id>      replace one exact string in a note's body
  delete <id>                     delete a note
  links <id>                      list backlinks to a note
  suggest [-limit N] <id>         suggest related notes to cross-link
  summary <id>                    summarize a note (needs --complete-url)
  reembed [-full]                 backfill missing vectors (-full: rebuild all)

Run "engram <subcommand> -h" for a subcommand's own options.
`

func cliHelp(_ context.Context, out io.Writer, _ []string) int {
	_, _ = fmt.Fprint(out, cliHelpText)
	return 0
}

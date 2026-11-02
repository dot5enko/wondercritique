package jsrun

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/tool"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/vfs"
)

// ToolName is the name of the tool returned by NewTool.
const ToolName = "run_js"

const toolDesc = `Run a JavaScript program in an isolated sandbox (QuickJS, modern ES syntax, strict mode) and get its console output and result.
- The value of the last expression is the result, returned as JSON. Top-level await works; a promise result is awaited.
- console.log/info/warn/error print one line per call (objects are shown as JSON).
- No network, no require/import of packages, no state kept between calls: each call starts from scratch.
- Limits: %s wall time, %d MiB memory, %d KiB console output (the rest is dropped), %d KiB result. Recursion deeper than about %d calls (about %d through callbacks like map/forEach) throws RangeError "Maximum call stack size exceeded"; walk deeper structures with a loop and an explicit stack.%s`

const toolFSDesc = `
- Workspace files are available through the global fs object (absolute paths like /work/notes.md):
  fs.read(path) → string; fs.write(path, text); fs.readJSON(path); fs.writeJSON(path, value);
  fs.list(dir, {recursive}) → [{path, isDir, size, version, modTime}]; fs.glob(pattern) → [path];
  fs.stat(path) → info or null; fs.exists(path); fs.delete(path).
  Failures throw an Error with a code: ENOENT, EISDIR, ENOTDIR, EROFS (read-only), EFBIG, EDQUOT, EINVAL, EBINARY.`

type toolInput struct {
	Code string `json:"code" jsonschema_description:"JavaScript source to run. End with an expression to return its value, e.g. fs.glob('/work/**/*.md').length"`
}

// NewTool returns the run_js tool. Scripts get fsys as the global fs (nil = no fs object); use
// vfs.ReadOnly to forbid writes. Script failures (exceptions, timeouts, …) are part of the
// tool output; only invalid input, a canceled context or an engine startup failure are errors.
func NewTool(runner *Runner, fsys vfs.FS) tool.Tool {
	cfg := runner.Config()
	fsDesc := ""
	if fsys != nil {
		fsDesc = toolFSDesc
	}
	calls, viaCallbacks := approxDepth(cfg.StackLimit)
	desc := fmt.Sprintf(toolDesc, cfg.Timeout, cfg.MemoryLimit>>20, cfg.MaxOutput>>10, cfg.MaxResult>>10,
		calls, viaCallbacks, fsDesc)

	return tool.New(ToolName, desc, func(ctx context.Context, in toolInput) (string, error) {
		res, err := runner.Run(ctx, Request{Code: in.Code, FS: fsys})
		if err != nil {
			if errors.Is(err, ErrInvalidRequest) {
				return "", errors.New(strings.TrimPrefix(err.Error(), ErrInvalidRequest.Error()+": "))
			}
			return "", err
		}
		return FormatResult(res, cfg), nil
	})
}

// FormatResult renders a result as plain text for a model: console output, then the result or
// the error, then the duration.
func FormatResult(res *Result, cfg Config) string {
	var b strings.Builder
	if res.Output != "" || res.OutputTruncated {
		b.WriteString("console:\n")
		b.WriteString(res.Output)
		if res.Output != "" && !strings.HasSuffix(res.Output, "\n") {
			b.WriteByte('\n')
		}
		if res.OutputTruncated {
			fmt.Fprintf(&b, "[console output truncated at %d bytes]\n", cfg.MaxOutput)
		}
		b.WriteByte('\n')
	}
	switch {
	case res.Error != nil:
		fmt.Fprintf(&b, "error (%s): %s\n", res.Error.Kind, res.Error.Message)
		if res.Error.Stack != "" {
			b.WriteString(res.Error.Stack)
			b.WriteByte('\n')
		}
	case res.Value != nil:
		b.WriteString("result: ")
		b.Write(res.Value)
		b.WriteByte('\n')
	default:
		b.WriteString("result: undefined\n")
	}
	fmt.Fprintf(&b, "(%s)", res.Duration.Round(time.Millisecond))
	return b.String()
}

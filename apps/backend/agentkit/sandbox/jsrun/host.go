package jsrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/sandbox/jsrun/internal/qjs"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/vfs"
)

// maxGlobResults caps fs.glob; more matches throw E2BIG instead of silently truncating.
const maxGlobResults = 10000

// output collects console output up to max bytes. It receives console calls and the guest's
// stdout/stderr; all writes happen on the goroutine running the engine.
type output struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

// Write implements io.Writer; it never fails, bytes beyond max are dropped.
func (o *output) Write(p []byte) (int, error) {
	n := len(p)
	if room := o.max - o.buf.Len(); n > room {
		o.truncated = true
		p = p[:max(room, 0)]
	}
	o.buf.Write(p)
	return n, nil
}

// text returns the output, without a character cut in half by truncation.
func (o *output) text() (string, bool) {
	b := o.buf.Bytes()
	if o.truncated {
		for i := 1; i <= utf8.UTFMax && i <= len(b); i++ {
			if utf8.RuneStart(b[len(b)-i]) {
				if !utf8.FullRune(b[len(b)-i:]) {
					b = b[:len(b)-i]
				}
				break
			}
		}
	}
	return string(b), o.truncated
}

// hostArgs returns the call's arguments; the caller must free them (the bridge clones them).
func hostArgs(this *qjs.This) ([]*qjs.Value, func()) {
	args := this.Args()
	return args, func() {
		for _, a := range args {
			a.Free()
		}
	}
}

// hostLog backs console.*: __wc_log(level, jsonText) → whether more output is accepted.
func (ex *execution) hostLog(this *qjs.This) (*qjs.Value, error) {
	args, free := hostArgs(this)
	defer free()
	if len(args) != 2 {
		return nil, errors.New("__wc_log: want 2 arguments")
	}
	level := args[0].String()
	var text string
	if err := json.Unmarshal([]byte(args[1].String()), &text); err != nil {
		return nil, fmt.Errorf("__wc_log: %w", err)
	}
	if !ex.out.truncated {
		switch level {
		case "warn":
			text = "[warn] " + text
		case "error":
			text = "[error] " + text
		}
		_, _ = ex.out.Write([]byte(text + "\n"))
	}
	return this.Context().NewBool(!ex.out.truncated), nil
}

// fsHost backs the JS fs object: __wc_fs(op, jsonArgs) → JSON envelope
// {"ok":true,"value":…} or {"ok":false,"code":"ENOENT","message":"…"}.
type fsHost struct {
	ctx  context.Context
	fsys vfs.FS
}

type fsArgs struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Pattern   string `json:"pattern"`
	Recursive bool   `json:"recursive"`
}

// fileInfo is the JS shape of vfs.FileInfo.
type fileInfo struct {
	Path    string `json:"path"`
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`
	Version int64  `json:"version,omitempty"`
	ModTime string `json:"modTime,omitempty"`
}

func toFileInfo(fi vfs.FileInfo) fileInfo {
	out := fileInfo{Path: fi.Path, IsDir: fi.IsDir, Size: fi.Size, Version: fi.Version}
	if !fi.ModTime.IsZero() {
		out.ModTime = fi.ModTime.UTC().Format(time.RFC3339Nano)
	}
	return out
}

// fsError is a failed fs call as seen by JS (Error with .code).
type fsError struct {
	code, message string
}

func (h *fsHost) call(this *qjs.This) (*qjs.Value, error) {
	args, free := hostArgs(this)
	defer free()
	if len(args) != 2 {
		return nil, errors.New("__wc_fs: want 2 arguments")
	}
	op := args[0].String()
	var in fsArgs
	if err := json.Unmarshal([]byte(args[1].String()), &in); err != nil {
		return nil, fmt.Errorf("__wc_fs: %w", err)
	}

	value, ferr := h.do(op, in)
	var resp []byte
	var err error
	if ferr != nil {
		resp, err = json.Marshal(map[string]any{"ok": false, "code": ferr.code, "message": ferr.message})
	} else {
		resp, err = json.Marshal(map[string]any{"ok": true, "value": value})
	}
	if err != nil {
		return nil, fmt.Errorf("__wc_fs: encode response: %w", err)
	}
	return this.Context().NewString(string(resp)), nil
}

func (h *fsHost) do(op string, in fsArgs) (any, *fsError) {
	p := absPath(in.Path)
	switch op {
	case "read":
		data, err := h.fsys.Read(h.ctx, p)
		if err != nil {
			return nil, translate(err, op, p)
		}
		if !vfs.IsText(data) {
			return nil, &fsError{"EBINARY", fmt.Sprintf("EBINARY: binary file (%d bytes) cannot be read as text, read '%s'", len(data), p)}
		}
		return string(data), nil
	case "write":
		if err := h.fsys.Write(h.ctx, p, []byte(in.Content)); err != nil {
			return nil, translate(err, op, p)
		}
		return nil, nil
	case "delete":
		if err := h.fsys.Delete(h.ctx, p); err != nil {
			return nil, translate(err, op, p)
		}
		return nil, nil
	case "stat":
		fi, err := h.fsys.Stat(h.ctx, p)
		if errors.Is(err, vfs.ErrNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, translate(err, op, p)
		}
		return toFileInfo(fi), nil
	case "list":
		infos, err := h.fsys.List(h.ctx, p, in.Recursive)
		if err != nil {
			return nil, translate(err, op, p)
		}
		out := make([]fileInfo, len(infos))
		for i, fi := range infos {
			out[i] = toFileInfo(fi)
		}
		return out, nil
	case "glob":
		pattern := absPath(in.Pattern)
		infos, err := vfs.Glob(h.ctx, h.fsys, pattern, maxGlobResults+1)
		if err != nil {
			return nil, translate(err, op, pattern)
		}
		if len(infos) > maxGlobResults {
			return nil, &fsError{"E2BIG", fmt.Sprintf("E2BIG: more than %d files match, glob '%s'; use a narrower pattern", maxGlobResults, pattern)}
		}
		out := make([]string, len(infos))
		for i, fi := range infos {
			out[i] = fi.Path
		}
		return out, nil
	default:
		return nil, &fsError{"EINVAL", "EINVAL: unknown fs operation " + op}
	}
}

// absPath makes relative paths relative to the root, like the glob tool does.
func absPath(p string) string {
	if !strings.HasPrefix(p, "/") {
		return "/" + p
	}
	return p
}

// translate maps vfs errors to Node-style codes and messages ("ENOENT: no such file or
// directory, read '/work/a.md'").
func translate(err error, op, path string) *fsError {
	code, desc := "EIO", err.Error()
	switch {
	case errors.Is(err, vfs.ErrNotFound):
		code, desc = "ENOENT", "no such file or directory"
	case errors.Is(err, vfs.ErrIsDir):
		code, desc = "EISDIR", "is a directory"
	case errors.Is(err, vfs.ErrNotDir):
		code, desc = "ENOTDIR", "not a directory (a file is in the way)"
	case errors.Is(err, vfs.ErrReadOnly):
		code, desc = "EROFS", "read-only file system"
	case errors.Is(err, vfs.ErrTooLarge):
		code, desc = "EFBIG", "file too large"
	case errors.Is(err, vfs.ErrQuota):
		code, desc = "EDQUOT", "workspace quota exceeded"
	case errors.Is(err, vfs.ErrInvalidPath), errors.Is(err, vfs.ErrBadPattern):
		code = "EINVAL"
	case errors.Is(err, context.DeadlineExceeded):
		code, desc = "ETIMEDOUT", "time limit reached"
	case errors.Is(err, context.Canceled):
		code, desc = "ECANCELED", "run canceled"
	}
	return &fsError{code, fmt.Sprintf("%s: %s, %s '%s'", code, desc, op, path)}
}

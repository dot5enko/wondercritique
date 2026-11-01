package jsrun

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/vfs"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/vfs/memory"
)

func newFS(t *testing.T, files map[string]string) *memory.FS {
	t.Helper()
	fsys := memory.New(vfs.Limits{MaxFileSize: 1 << 20, MaxTotalSize: 4 << 20, MaxFiles: 50})
	for p, c := range files {
		if err := fsys.Write(context.Background(), p, []byte(c)); err != nil {
			t.Fatal(err)
		}
	}
	return fsys
}

func TestFSReadWrite(t *testing.T) {
	fsys := newFS(t, map[string]string{
		"/work/a.md":         "# A\n",
		"/work/sub/b.json":   `{"n": 2}`,
		"/skills/x/SKILL.md": "skill",
		"/bin.dat":           "\x00\x01",
	})
	r := New(Config{})
	res := run(t, r, Request{FS: fsys, Code: `
		const a = fs.read("/work/a.md");
		fs.write("/work/out/c.txt", a + "more\n");
		fs.writeJSON("/work/out/d.json", {b: fs.readJSON("/work/sub/b.json").n * 21});
		fs.write("work/rel.txt", "relative");
		fs.delete("/work/a.md");
		({
			a,
			c: fs.read("/work/out/c.txt"),
			d: fs.read("/work/out/d.json"),
			rel: fs.read("/work/rel.txt"),
			exists: [fs.exists("/work/a.md"), fs.exists("/work/out"), fs.exists("/nope")],
			stat: [fs.stat("/work/out").isDir, fs.stat("/work/out/c.txt").size, fs.stat("/work/out/c.txt").version, fs.stat("/missing")],
			list: fs.list("/work").map(f => f.path + (f.isDir ? "/" : "")),
			listDefault: fs.list().map(f => f.path),
			recursive: fs.list("/work", {recursive: true}).map(f => f.path),
			glob: fs.glob("/**/*.md"),
			globRel: fs.glob("work/**/*.json"),
		})`})
	wantValue(t, res, `{"a":"# A\n","c":"# A\nmore\n","d":"{\n  \"b\": 42\n}\n","rel":"relative",`+
		`"exists":[false,true,false],"stat":[true,9,1,null],`+
		`"list":["/work/out/","/work/rel.txt","/work/sub/"],"listDefault":["/bin.dat","/skills","/work"],`+
		`"recursive":["/work/out/c.txt","/work/out/d.json","/work/rel.txt","/work/sub/b.json"],`+
		`"glob":["/skills/x/SKILL.md"],"globRel":["/work/out/d.json","/work/sub/b.json"]}`)

	data, err := fsys.Read(context.Background(), "/work/out/d.json")
	if err != nil || string(data) != "{\n  \"b\": 42\n}\n" {
		t.Fatalf("written file = %q, %v", data, err)
	}

	// File info shape.
	res = run(t, r, Request{FS: fsys, Code: `fs.stat("/work/out/c.txt")`})
	if res.Error != nil || !strings.Contains(string(res.Value), `"path":"/work/out/c.txt","isDir":false,"size":9,"version":1,"modTime":"`) {
		t.Fatalf("stat = %s %+v", res.Value, res.Error)
	}
}

func TestFSErrors(t *testing.T) {
	fsys := newFS(t, map[string]string{"/work/a.md": "a", "/bin.dat": "\x00\x01"})
	r := New(Config{})
	res := run(t, r, Request{FS: fsys, Code: `
		const codes = {};
		const attempt = (name, fn) => { try { fn(); codes[name] = "no error" } catch (e) { codes[name] = (e.code || e.name) + " | " + e.message } };
		attempt("missing", () => fs.read("/work/nope.md"));
		attempt("dir", () => fs.read("/work"));
		attempt("notdir", () => fs.write("/work/a.md/x", "x"));
		attempt("writeDir", () => fs.write("/work", "x"));
		attempt("binary", () => fs.read("/bin.dat"));
		attempt("invalid", () => fs.read("/work/../etc/passwd"));
		attempt("badGlob", () => fs.glob("/work/[")); 
		attempt("big", () => fs.write("/work/big.txt", "x".repeat((1 << 20) + 1)));
		attempt("deleteMissing", () => fs.delete("/nope"));
		attempt("listFile", () => fs.list("/work/a.md"));
		attempt("nonString", () => fs.write("/work/n.txt", {a: 1}));
		attempt("pathType", () => fs.read(42));
		attempt("readJSON", () => fs.readJSON("/work/a.md"));
		codes`})
	if res.Error != nil {
		t.Fatalf("error: %+v", res.Error)
	}
	want := map[string]string{
		"missing":       `ENOENT | ENOENT: no such file or directory, read '/work/nope.md'`,
		"dir":           `EISDIR | EISDIR: is a directory, read '/work'`,
		"notdir":        `ENOTDIR | ENOTDIR: not a directory (a file is in the way), write '/work/a.md/x'`,
		"writeDir":      `EISDIR | EISDIR: is a directory, write '/work'`,
		"binary":        `EBINARY | EBINARY: binary file (2 bytes) cannot be read as text, read '/bin.dat'`,
		"invalid":       `EINVAL | EINVAL: `,
		"badGlob":       `EINVAL | EINVAL: `,
		"big":           `EFBIG | EFBIG: file too large, write '/work/big.txt'`,
		"deleteMissing": `ENOENT | ENOENT: no such file or directory, delete '/nope'`,
		"listFile":      `ENOTDIR | ENOTDIR: not a directory (a file is in the way), list '/work/a.md'`,
		"nonString":     `TypeError | fs.write: content must be a string`,
		"pathType":      `TypeError | fs.read: path must be a string`,
		"readJSON":      `SyntaxError | fs.readJSON /work/a.md: `,
	}
	got := string(res.Value)
	for k, w := range want {
		if !strings.Contains(got, `"`+k+`":"`+strings.ReplaceAll(w, `'`, `'`)) {
			t.Errorf("%s: want prefix %q in %s", k, w, got)
		}
	}

	// Uncaught fs errors fail the script with the code in the message.
	res = run(t, r, Request{FS: fsys, Code: `fs.read("/nope")`})
	wantError(t, res, KindException, "ENOENT: no such file or directory, read '/nope'")
}

func TestFSReadOnlyAndQuota(t *testing.T) {
	fsys := newFS(t, map[string]string{"/a.txt": "a"})
	r := New(Config{})

	res := run(t, r, Request{FS: vfs.ReadOnly(fsys), Code: `
		const out = [fs.read("/a.txt")];
		try { fs.write("/a.txt", "b") } catch (e) { out.push(e.code) }
		try { fs.delete("/a.txt") } catch (e) { out.push(e.code) }
		out`})
	wantValue(t, res, `["a","EROFS","EROFS"]`)

	res = run(t, r, Request{FS: fsys, Code: `
		const chunk = "x".repeat(1 << 20);
		let n = 0;
		try { for (;;) { fs.write("/f" + n + ".txt", chunk); n++ } } catch (e) { [e.code, n] }`})
	wantValue(t, res, `["EDQUOT",3]`)
}

func TestFSAbsent(t *testing.T) {
	res := runCode(t, `typeof fs`)
	wantValue(t, res, `"undefined"`)
}

func TestFSHonorsDeadline(t *testing.T) {
	fsys := newFS(t, map[string]string{"/a.txt": "a"})
	r := New(Config{Timeout: 200 * time.Millisecond})
	res := run(t, r, Request{FS: fsys, Code: `for (;;) { try { fs.read("/a.txt") } catch (e) {} }`})
	wantError(t, res, KindTimeout, "200ms")
}

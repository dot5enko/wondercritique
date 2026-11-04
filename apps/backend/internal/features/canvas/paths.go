package canvas

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/vfs"
)

// Path rules of the canvas tree: absolute paths of lowercase [a-z0-9-]
// segments (≤ 48 chars); files end in ".html"; at most MaxDepth folder levels.
const (
	MaxDepth      = 4
	maxSegmentLen = 48
	fileExt       = ".html"
)

var segmentRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// ErrBadPath is returned for paths that break the canvas path rules.
var ErrBadPath = errors.New("canvas: invalid path")

// stopWords are dropped from briefs when they become folder/file names.
var stopWords = map[string]bool{
	"a": true, "an": true, "the": true, "of": true, "for": true, "with": true, "and": true, "or": true,
	"to": true, "in": true, "on": true, "at": true, "me": true, "my": true, "it": true, "its": true,
	"this": true, "that": true, "these": true, "those": true, "please": true, "make": true, "give": true,
	"create": true, "design": true, "build": true, "some": true, "us": true, "can": true, "you": true,
	"i": true, "want": true, "need": true, "like": true, "is": true, "be": true, "them": true, "into": true,
}

// Slug turns free text into one path segment: lowercase ASCII letters, digits
// and single hyphens, ≤ 48 chars; "design" when nothing is left.
func Slug(s string) string {
	return slugWords(s, 0, false)
}

// BriefSlug names a design after its brief: the first few meaningful words.
func BriefSlug(brief string) string {
	return slugWords(brief, 4, true)
}

func slugWords(s string, maxWords int, dropStop bool) string {
	var words []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			words = append(words, cur.String())
			cur.Reset()
		}
	}
	for _, r := range norm.NFKD.String(strings.ToLower(s)) {
		switch {
		case unicode.Is(unicode.Mn, r):
			// combining mark of a decomposed letter: drop (é → e)
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			cur.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	if dropStop {
		var kept []string
		for _, w := range words {
			if !stopWords[w] {
				kept = append(kept, w)
			}
		}
		if len(kept) > 0 {
			words = kept
		}
	}
	if maxWords > 0 && len(words) > maxWords {
		words = words[:maxWords]
	}
	out := ""
	for _, w := range words {
		next := w
		if out != "" {
			next = out + "-" + w
		}
		if len(next) > maxSegmentLen {
			if out == "" {
				out = w[:maxSegmentLen]
			}
			break
		}
		out = next
	}
	if out == "" {
		return "design"
	}
	return out
}

// segments splits a cleaned path ("/" → none).
func segments(clean string) []string {
	if clean == "/" {
		return nil
	}
	return strings.Split(clean[1:], "/")
}

// CleanFolder validates a folder path strictly ("/" is the root folder).
func CleanFolder(p string) (string, error) {
	c, err := vfs.Clean(p)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrBadPath, err)
	}
	segs := segments(c)
	if len(segs) > MaxDepth {
		return "", fmt.Errorf("%w: %s is nested deeper than %d folders", ErrBadPath, c, MaxDepth)
	}
	for _, s := range segs {
		if len(s) > maxSegmentLen || !segmentRe.MatchString(s) {
			return "", fmt.Errorf("%w: %q: folder names use lowercase letters, digits and hyphens", ErrBadPath, s)
		}
	}
	return c, nil
}

// CleanFile validates a file path strictly.
func CleanFile(p string) (string, error) {
	c, err := vfs.Clean(p)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrBadPath, err)
	}
	if c == "/" || !strings.HasSuffix(c, fileExt) {
		return "", fmt.Errorf("%w: %s: component files end in %s", ErrBadPath, c, fileExt)
	}
	if _, err := CleanFolder(vfs.Dir(c)); err != nil {
		return "", err
	}
	stem := strings.TrimSuffix(vfs.Base(c), fileExt)
	if len(stem) > maxSegmentLen || !segmentRe.MatchString(stem) {
		return "", fmt.Errorf("%w: %q: file names use lowercase letters, digits and hyphens", ErrBadPath, vfs.Base(c))
	}
	return c, nil
}

// NormalizeFolder turns a loosely written folder ("Pricing/Dark Theme") into a
// valid folder path by slugging every segment. Empty input is the root.
func NormalizeFolder(p string) (string, error) {
	var segs []string
	for _, s := range strings.Split(p, "/") {
		if strings.TrimSpace(s) == "" {
			continue
		}
		segs = append(segs, Slug(s))
	}
	return CleanFolder("/" + strings.Join(segs, "/"))
}

// NormalizeName turns a loosely written file name ("Tiers card.HTML") into a
// valid file name (no folder).
func NormalizeName(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	if strings.HasSuffix(strings.ToLower(name), fileExt) {
		name = name[:len(name)-len(fileExt)]
	}
	return Slug(name) + fileExt
}

// NormalizeFile normalizes a file path (folder segments + file name).
func NormalizeFile(p string) (string, error) {
	p = strings.TrimSpace(p)
	dir, name := "", p
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		dir, name = p[:i], p[i+1:]
	}
	folder, err := NormalizeFolder(dir)
	if err != nil {
		return "", err
	}
	return Join(folder, NormalizeName(name)), nil
}

// Join appends a name to a folder.
func Join(folder, name string) string {
	if folder == "/" {
		return "/" + name
	}
	return folder + "/" + name
}

// Depth is the number of folder levels of a folder path.
func Depth(folder string) int { return len(segments(folder)) }

// within reports whether path p lies below folder (or equals it).
func within(p, folder string) bool { return vfs.IsWithin(p, folder) }

// uniqueFile returns p, or p with "-2", "-3", … appended to the stem when taken.
func uniqueFile(p string, taken func(string) bool) string {
	if !taken(p) {
		return p
	}
	dir, stem := vfs.Dir(p), strings.TrimSuffix(vfs.Base(p), fileExt)
	for n := 2; ; n++ {
		cand := Join(dir, suffixed(stem, n)+fileExt)
		if !taken(cand) {
			return cand
		}
	}
}

// uniqueFolder returns f, or f with "-2", "-3", … appended to its last segment
// when taken.
func uniqueFolder(f string, taken func(string) bool) string {
	if f == "/" || !taken(f) {
		return f
	}
	dir, last := vfs.Dir(f), vfs.Base(f)
	for n := 2; ; n++ {
		cand := Join(dir, suffixed(last, n))
		if !taken(cand) {
			return cand
		}
	}
}

func suffixed(seg string, n int) string {
	suffix := fmt.Sprintf("-%d", n)
	if len(seg)+len(suffix) > maxSegmentLen {
		seg = strings.TrimRight(seg[:maxSegmentLen-len(suffix)], "-")
	}
	return seg + suffix
}

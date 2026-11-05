// Package styleguide turns sample images into markdown style guides: the
// upload is stored, the seeded vision agent (studio.StyleguideAgentSlug)
// writes the guide in the background, and chat threads pick one with their
// first message (engine: runs.styleguide_id; agentrun: system prompt and
// /styleguide/STYLEGUIDE.md).
package styleguide

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/dot5enko/wondercritique/apps/backend/internal/features/agentrun"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/blobs"
	"github.com/dot5enko/wondercritique/apps/backend/internal/features/studio"
	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/apperr"
)

const (
	// MaxImages is how many sample images one guide may be made from.
	MaxImages = 4
	// MaxImageBytes caps one sample image.
	MaxImageBytes = 8 << 20
	// MaxNameLen / MaxNotesLen / MaxMarkdownLen cap the text fields.
	MaxNameLen     = 200
	MaxNotesLen    = 4000
	MaxMarkdownLen = 100_000
	// generateTimeout bounds one generation.
	generateTimeout = 10 * time.Minute
	// defaultName is used when neither the user nor the guide names it.
	defaultName = "Style guide"
)

// Runner runs the generator agent (*agentrun.Runtime; tests fake it).
type Runner interface {
	RunNode(ctx context.Context, req agentrun.NodeRequest) (*agentrun.NodeResult, error)
}

// Viewer is the caller: guides are visible to their owner and to admins.
type Viewer struct {
	UserID string
	Admin  bool
}

// Image is one uploaded sample.
type Image struct {
	Data []byte
	MIME string
}

// Service stores style guides and generates them in the background.
type Service struct {
	db     *gorm.DB
	blobs  *blobs.Store
	defs   *studio.Store
	agents Runner
	log    *slog.Logger

	root context.Context
	stop context.CancelFunc
	wg   sync.WaitGroup
}

// NewService builds the service; Close stops running generations.
func NewService(db *gorm.DB, bl *blobs.Store, defs *studio.Store, agents Runner, log *slog.Logger) *Service {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	root, stop := context.WithCancel(context.Background())
	return &Service{db: db, blobs: bl, defs: defs, agents: agents, log: log, root: root, stop: stop}
}

// Close cancels running generations (they end failed) and waits for them.
func (s *Service) Close(ctx context.Context) error {
	s.stop()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Recover marks generations a previous process left unfinished as failed.
func (s *Service) Recover(ctx context.Context) (int64, error) {
	res := s.db.WithContext(ctx).Model(&models.Styleguide{}).
		Where("status = ?", models.StyleguideGenerating).
		Updates(map[string]any{"status": models.StyleguideFailed, "error": "the server restarted during generation; try again"})
	return res.RowsAffected, res.Error
}

// List returns the viewer's own guides, newest first.
func (s *Service) List(ctx context.Context, v Viewer) ([]models.Styleguide, error) {
	var out []models.Styleguide
	err := s.db.WithContext(ctx).Where("owner_id = ?", v.UserID).
		Order("created_at DESC, id DESC").Limit(200).Find(&out).Error
	if err != nil {
		return nil, apperr.Internal(err)
	}
	return out, nil
}

// Get loads a guide the viewer may see (404 otherwise).
func (s *Service) Get(ctx context.Context, v Viewer, id string) (*models.Styleguide, error) {
	q := s.db.WithContext(ctx).Where("id = ?", id)
	if !v.Admin {
		q = q.Where("owner_id = ?", v.UserID)
	}
	var sg models.Styleguide
	if err := q.First(&sg).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperr.NotFound("style guide not found")
		}
		return nil, apperr.Internal(err)
	}
	return &sg, nil
}

// Create stores the samples and starts generating the guide.
func (s *Service) Create(ctx context.Context, v Viewer, name, notes string, images []Image) (*models.Styleguide, error) {
	name, notes = strings.TrimSpace(name), strings.TrimSpace(notes)
	fields := map[string]string{}
	switch {
	case len(images) == 0:
		fields["images"] = "add at least one image"
	case len(images) > MaxImages:
		fields["images"] = fmt.Sprintf("at most %d images", MaxImages)
	}
	if len([]rune(name)) > MaxNameLen {
		fields["name"] = fmt.Sprintf("must be at most %d characters", MaxNameLen)
	}
	if len([]rune(notes)) > MaxNotesLen {
		fields["notes"] = fmt.Sprintf("must be at most %d characters", MaxNotesLen)
	}
	if len(fields) > 0 {
		return nil, apperr.Invalid("invalid style guide", fields)
	}
	sg := &models.Styleguide{
		Base:    models.Base{ID: models.NewID()},
		OwnerID: v.UserID, Name: name, Notes: notes,
		Status: models.StyleguideGenerating,
	}
	for _, img := range images {
		sha, err := s.blobs.Put(ctx, img.Data, img.MIME)
		if err != nil {
			return nil, apperr.Invalid("invalid image", map[string]string{"images": err.Error()})
		}
		sg.Images = append(sg.Images, blobs.URL(sha))
	}
	if err := s.db.WithContext(ctx).Create(sg).Error; err != nil {
		return nil, apperr.Internal(fmt.Errorf("styleguide: create: %w", err))
	}
	s.launch(*sg)
	return sg, nil
}

// Regenerate writes the guide again from its samples (not while generating).
func (s *Service) Regenerate(ctx context.Context, v Viewer, id string) (*models.Styleguide, error) {
	sg, err := s.Get(ctx, v, id)
	if err != nil {
		return nil, err
	}
	res := s.db.WithContext(ctx).Model(&models.Styleguide{}).
		Where("id = ? AND status <> ?", sg.ID, models.StyleguideGenerating).
		Updates(map[string]any{"status": models.StyleguideGenerating, "error": ""})
	if res.Error != nil {
		return nil, apperr.Internal(res.Error)
	}
	if res.RowsAffected == 0 {
		return nil, apperr.Conflict("the style guide is being generated")
	}
	sg.Status, sg.Error = models.StyleguideGenerating, ""
	s.launch(*sg)
	return sg, nil
}

// Update edits the name and/or markdown of a finished guide (nil = keep).
func (s *Service) Update(ctx context.Context, v Viewer, id string, name, markdown *string) (*models.Styleguide, error) {
	sg, err := s.Get(ctx, v, id)
	if err != nil {
		return nil, err
	}
	if sg.Status == models.StyleguideGenerating {
		return nil, apperr.Conflict("the style guide is being generated")
	}
	fields := map[string]string{}
	if name != nil {
		n := strings.TrimSpace(*name)
		switch {
		case n == "":
			fields["name"] = "is required"
		case len([]rune(n)) > MaxNameLen:
			fields["name"] = fmt.Sprintf("must be at most %d characters", MaxNameLen)
		}
		sg.Name = n
	}
	if markdown != nil {
		m := strings.TrimSpace(*markdown)
		switch {
		case m == "":
			fields["markdown"] = "is required"
		case len(m) > MaxMarkdownLen:
			fields["markdown"] = fmt.Sprintf("must be at most %d characters", MaxMarkdownLen)
		}
		sg.Markdown = m
		// A hand-written guide is usable even when generation failed.
		sg.Status, sg.Error = models.StyleguideReady, ""
	}
	if len(fields) > 0 {
		return nil, apperr.Invalid("invalid style guide", fields)
	}
	if err := s.db.WithContext(ctx).Model(sg).Select("name", "markdown", "status", "error").Updates(sg).Error; err != nil {
		return nil, apperr.Internal(err)
	}
	return sg, nil
}

// Delete soft-deletes a guide. Chats that picked it keep following it.
func (s *Service) Delete(ctx context.Context, v Viewer, id string) error {
	sg, err := s.Get(ctx, v, id)
	if err != nil {
		return err
	}
	if err := s.db.WithContext(ctx).Delete(sg).Error; err != nil {
		return apperr.Internal(err)
	}
	return nil
}

// Image returns sample n (0-based) of a guide.
func (s *Service) Image(ctx context.Context, v Viewer, id string, n int) (*blobs.Blob, error) {
	sg, err := s.Get(ctx, v, id)
	if err != nil {
		return nil, err
	}
	if n < 0 || n >= len(sg.Images) {
		return nil, apperr.NotFound("image not found")
	}
	sha, err := blobs.ParseURL(sg.Images[n])
	if err != nil {
		return nil, apperr.Internal(err)
	}
	return s.blobs.Get(ctx, sha)
}

/* ---------- generation ---------- */

func (s *Service) launch(sg models.Styleguide) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ctx, cancel := context.WithTimeout(s.root, generateTimeout)
		defer cancel()
		name, md, err := s.generate(ctx, sg)
		upd := map[string]any{}
		if err != nil {
			s.log.Warn("styleguide: generation failed", "id", sg.ID, "err", err)
			upd["status"], upd["error"] = models.StyleguideFailed, failureText(ctx, err)
		} else {
			upd["status"], upd["error"], upd["markdown"] = models.StyleguideReady, "", md
			if sg.Name == "" {
				upd["name"] = name
			}
		}
		// A fresh context: the root may be cancelled (shutdown) by now.
		wctx, wcancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer wcancel()
		if err := s.db.WithContext(wctx).Model(&models.Styleguide{}).Where("id = ?", sg.ID).Updates(upd).Error; err != nil {
			s.log.Error("styleguide: save result", "id", sg.ID, "err", err)
		}
	}()
}

// generate runs the generator agent on the samples and returns the guide's
// name (from its heading) and markdown.
func (s *Service) generate(ctx context.Context, sg models.Styleguide) (string, string, error) {
	def, err := s.defs.GetBySlug(ctx, studio.KindAgent, studio.StyleguideAgentSlug)
	if err != nil {
		return "", "", apperr.Unavailable("the " + studio.StyleguideAgentSlug + " agent is missing")
	}
	name, notes := sg.Name, sg.Notes
	if name == "" {
		name = "(none given: pick a short fitting name)"
	}
	if notes == "" {
		notes = "(none)"
	}
	out, err := s.agents.RunNode(ctx, agentrun.NodeRequest{
		AgentID: def.ID,
		Samples: map[string]any{"images": sg.Images, "name": name, "notes": notes},
		Owner:   sg.OwnerID,
		Title:   "Style guide · " + firstNonEmpty(sg.Name, sg.ID),
	})
	if err != nil {
		return "", "", err
	}
	md := CleanMarkdown(out.Text)
	if md == "" {
		return "", "", errors.New("the model returned an empty style guide")
	}
	if len(md) > MaxMarkdownLen {
		md = md[:MaxMarkdownLen]
	}
	return NameOf(md), md, nil
}

func failureText(ctx context.Context, err error) string {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return fmt.Sprintf("generation did not finish within %s", generateTimeout)
	case errors.Is(err, context.Canceled):
		return "generation was stopped (server shutdown); try again"
	}
	if ae, ok := apperr.As(err); ok {
		return ae.Message
	}
	return err.Error()
}

var outerFence = regexp.MustCompile("(?s)^```(?:markdown|md)?[ \t]*\n(.*?)\n?```$")

// CleanMarkdown trims the answer and unwraps a fence around the whole of it.
func CleanMarkdown(text string) string {
	s := strings.TrimSpace(text)
	if m := outerFence.FindStringSubmatch(s); m != nil {
		s = strings.TrimSpace(m[1])
	}
	return s
}

var headingRe = regexp.MustCompile(`(?m)^#[ \t]+(.+?)[ \t#]*$`)

// NameOf names a guide after its first heading ("Acme style guide" → "Acme").
func NameOf(md string) string {
	m := headingRe.FindStringSubmatch(md)
	if m == nil {
		return defaultName
	}
	name := strings.TrimSpace(m[1])
	lower := strings.ToLower(name)
	for _, suffix := range []string{" style guide", " styleguide", " design system"} {
		if strings.HasSuffix(lower, suffix) && len(name) > len(suffix) {
			name = strings.TrimSpace(name[:len(name)-len(suffix)])
			break
		}
	}
	if r := []rune(name); len(r) > MaxNameLen {
		name = string(r[:MaxNameLen])
	}
	if name == "" {
		return defaultName
	}
	return name
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

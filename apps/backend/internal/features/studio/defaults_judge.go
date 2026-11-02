// The built-in vision judge used by pick_best (tool and pipeline node).
package studio

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/apperr"
	"gorm.io/gorm"
)

const (
	judgeName        = "Vision judge"
	judgeDescription = "Compares several rendered candidates side by side and picks the best (used by pick_best)."
)

// JudgeFieldsSchema is the custom output contract of a pick_best judge:
// winner is the 1-based number of the best candidate.
const JudgeFieldsSchema = `{
  "type": "object",
  "properties": {
    "winner": {"type": "integer", "description": "1-based number of the best candidate"},
    "ranking": {
      "type": "array",
      "description": "Every candidate, best first",
      "items": {
        "type": "object",
        "properties": {
          "candidate": {"type": "integer", "description": "1-based candidate number"},
          "score": {"type": "number", "description": "Quality 0-10"},
          "reason": {"type": "string", "description": "One or two sentences"}
        },
        "required": ["candidate", "score", "reason"]
      }
    },
    "summary": {"type": "string", "description": "Why the winner beats the others"}
  },
  "required": ["winner", "ranking", "summary"]
}`

const judgeSystem = `You are an expert UI design judge. You receive several candidate HTML
components for the same brief, each rendered as one or more screenshots and
labelled "Candidate N".

Compare them side by side: fit to the brief, visual hierarchy, layout and
spacing, typography, color and contrast, originality and polish. Rendering
defects (clipped content, overflow, broken layout, unreadable text) weigh
heavily. Do not favour a candidate because of its position.

Call submit_fields with:
- winner: the number of the best candidate
- ranking: every candidate, best first, with a 0-10 score and a short reason
- summary: why the winner beats the others`

func judgeSeed(modelSetID string) AgentBody {
	return AgentBody{
		ModelSetID:     modelSetID,
		SystemPrompt:   judgeSystem,
		InputTemplate:  "## Brief\n\n{{brief}}\n\n## Criteria\n\n{{criteria}}",
		OutputContract: OutputContract{Type: "custom", FieldsSchema: json.RawMessage(JudgeFieldsSchema)},
	}
}

// ensureJudge adds the vision-judge agent to an already-seeded install that
// predates it (uses the "default" model set; skipped when that is gone).
// Note: deleting vision-judge re-creates it on the next start.
func ensureJudge(ctx context.Context, store *Store, log *slog.Logger) error {
	if _, err := store.GetBySlug(ctx, KindAgent, DefaultJudgeSlug); err == nil {
		return nil
	} else if !isNotFound(err) {
		return err
	}
	ms, err := store.GetBySlug(ctx, KindModelSet, "default")
	if err != nil {
		log.Info("studio: vision-judge not seeded (no default model set)")
		return nil
	}
	raw, err := json.Marshal(judgeSeed(ms.ID))
	if err != nil {
		return err
	}
	def, err := store.Create(ctx, KindAgent, DefIdentity{Slug: DefaultJudgeSlug, Name: judgeName, Description: judgeDescription}, raw)
	if err != nil {
		return err
	}
	if _, _, err := store.Publish(ctx, KindAgent, def.ID, "seeded", "system", raw); err != nil {
		return err
	}
	log.Info("studio: seeded vision-judge agent")
	return nil
}

func isNotFound(err error) bool {
	var ae *apperr.Error
	return errors.Is(err, gorm.ErrRecordNotFound) || (errors.As(err, &ae) && ae.Status == 404)
}

package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/dot5enko/wondercritique/apps/backend/internal/models"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/apperr"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/dbx"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/httpx"
	"github.com/go-chi/chi/v5"
)

// PipelineRef identifies a pipeline that references an agent.
type PipelineRef struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// AgentUsage maps agent def id → the pipelines whose CURRENT content
// references it. "Current" = the draft plus the latest published version
// (older immutable versions are history and don't block deletion).
func (s *Store) AgentUsage(ctx context.Context) (map[string][]PipelineRef, error) {
	pipelines, err := s.List(ctx, KindPipeline)
	if err != nil {
		return nil, err
	}
	usage := map[string][]PipelineRef{}
	for _, p := range pipelines {
		bodies := []json.RawMessage{}
		if len(p.Draft) > 0 {
			bodies = append(bodies, p.Draft)
		}
		if p.LatestVer != nil {
			v, err := s.Version(ctx, p.ID, *p.LatestVer)
			if err != nil {
				return nil, err
			}
			bodies = append(bodies, v.Body)
		}
		seen := map[string]bool{}
		ref := PipelineRef{ID: p.ID, Slug: p.Slug, Name: p.Name}
		for _, raw := range bodies {
			// Lenient decode: only node configs matter here.
			var body struct {
				Nodes []struct {
					Type   string         `json:"type"`
					Config map[string]any `json:"config"`
				} `json:"nodes"`
			}
			if json.Unmarshal(raw, &body) != nil {
				continue
			}
			for _, n := range body.Nodes {
				if n.Type != "agent" {
					continue
				}
				if id, _ := n.Config["agent_id"].(string); id != "" && !seen[id] {
					seen[id] = true
					usage[id] = append(usage[id], ref)
				}
			}
		}
	}
	return usage, nil
}

// DeleteAgent removes an agent and all its versions. It refuses (409) while
// any pipeline still references the agent.
func (s *Store) DeleteAgent(ctx context.Context, id string) error {
	return dbx.Tx(ctx, s.db, func(ctx context.Context) error {
		def, err := s.getLocked(ctx, KindAgent, id)
		if err != nil {
			return err
		}
		usage, err := s.AgentUsage(ctx)
		if err != nil {
			return err
		}
		if refs := usage[def.ID]; len(refs) > 0 {
			names := make([]string, len(refs))
			for i, r := range refs {
				names[i] = r.Name
			}
			return apperr.Conflict(fmt.Sprintf("agent %q is used in pipelines: %s", def.Name, strings.Join(names, ", ")))
		}
		// Hard delete: soft-deleted rows would keep the (kind, slug) unique
		// index occupied and block re-creating the slug.
		if err := dbx.Conn(ctx, s.db).Unscoped().Where("def_id = ?", def.ID).
			Delete(&models.StudioDefVersion{}).Error; err != nil {
			return fmt.Errorf("studio: delete versions: %w", err)
		}
		if err := dbx.Conn(ctx, s.db).Unscoped().Where("id = ?", def.ID).
			Delete(&models.StudioDef{}).Error; err != nil {
			return fmt.Errorf("studio: delete def: %w", err)
		}
		return nil
	})
}

type agentUsageResponse struct {
	Usage map[string][]PipelineRef `json:"usage"`
}

// agentUsage serves GET /studio/agents/usage.
func (c *Controller) agentUsage(w http.ResponseWriter, r *http.Request) error {
	usage, err := c.store.AgentUsage(r.Context())
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, agentUsageResponse{Usage: usage})
}

// deleteDef serves DELETE /studio/{kind}/{id}. Only agents are deletable.
func (c *Controller) deleteDef(w http.ResponseWriter, r *http.Request) error {
	kind, err := kindParam(r)
	if err != nil {
		return err
	}
	if kind != KindAgent {
		return invalid("only agents can be deleted")
	}
	if err := c.store.DeleteAgent(r.Context(), chi.URLParam(r, "id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

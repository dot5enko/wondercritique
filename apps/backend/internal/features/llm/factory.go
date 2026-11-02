// Adapter construction from the parsed models.dev catalog (plan §3.2).
// Built via the exact agentkit adapter constructors from Workstream A.
package llm

import (
	"fmt"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm/anthropic"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm/chat"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/llm/responses"
	"github.com/dot5enko/wondercritique/apps/backend/internal/platform/apperr"
)

// buildAdapter constructs the llm.Provider for a catalog provider. DefaultModel
// stays empty: the runtime always sends explicit model ids.
func (s *Service) buildAdapter(p *CatalogProvider, apiKey, baseURL string) (llm.Provider, error) {
	switch p.Adapter {
	case AdapterResponses:
		return responses.New(responses.Config{
			APIKey: apiKey, BaseURL: baseURL, Name: p.ID,
		}), nil
	case AdapterChat:
		return chat.New(chat.Config{
			APIKey: apiKey, BaseURL: baseURL, Name: p.ID,
		}), nil
	case AdapterAnthropic:
		return anthropic.New(anthropic.Config{
			APIKey: apiKey, BaseURL: baseURL, Name: p.ID,
		}), nil
	default: // AdapterNone and anything unrecognised
		return nil, apperr.Unavailable(fmt.Sprintf("provider %q uses an unsupported adapter", p.ID))
	}
}

package memory_test

import (
	"testing"

	"github.com/dot5enko/wondercritique/apps/backend/agentkit/store"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/store/memory"
	"github.com/dot5enko/wondercritique/apps/backend/agentkit/store/storetest"
)

func TestConformance(t *testing.T) {
	storetest.Run(t, func(*testing.T) store.Store { return memory.New() })
}

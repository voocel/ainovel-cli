package profile

import (
	"context"

	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/infra/capability/prompt"
	"github.com/voocel/ainovel-cli/internal/infra/store"
)

// Compiler assembles and freezes the LLM context and prompt for creative tasks.
type Compiler struct {
	store    *store.Store
	projects *projectdoc.Repository
	prompts  *prompt.Registry
	runtime  any
}

func New(s *store.Store, projects *projectdoc.Repository, runtime any) *Compiler {
	return &Compiler{store: s, projects: projects, prompts: prompt.NewRegistry(s), runtime: runtime}
}
func (s *Compiler) Load(ctx context.Context, digest string) (prompt.Compiled, error) {
	return s.prompts.Load(ctx, digest)
}

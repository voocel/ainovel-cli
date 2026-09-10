package bootstrap_test

import (
	"strconv"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/app/task"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain/change"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/domain/operation"
	"github.com/voocel/ainovel-cli/internal/infra/store"
)

type testApp struct {
	*bootstrap.App
	store      *store.Store
	changes    *change.Engine
	operations *operation.Engine
	now        func() time.Time
}

func newTestApp(s *store.Store, options ...bootstrap.Options) *testApp {
	var opts bootstrap.Options
	if len(options) > 0 {
		opts = options[0]
	}
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	fixture := &testApp{store: s, changes: change.New(s), operations: operation.NewEngine(s, opts.Contracts...), now: now}
	opts.Now = func() time.Time { return fixture.now() }
	fixture.App = bootstrap.New(s, opts)
	return fixture
}
func newTestAppWithExecutor(s *store.Store, e operation.Executor) *testApp {
	return newTestApp(s, bootstrap.Options{Executors: task.ExecutorSet{LLM: e}})
}
func newTestAppWithExecutors(s *store.Store, e task.ExecutorSet, contracts ...operation.VerdictContract) *testApp {
	return newTestApp(s, bootstrap.Options{Executors: e, Contracts: contracts})
}

// Slot IDs are asserted by integration tests because recovery preserves them.
func runQuickID(runID string, parts ...string) string { return runID + ":" + strings.Join(parts, ":") }
func reviewOperationID(runID string, revision model.Revision) string {
	return runQuickID(runID, "review", "r"+strconv.FormatInt(int64(revision), 10))
}

package creation_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/creation"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/store"
)

type goalFunc func(context.Context, model.CreationRun) (creation.Decision, error)

type failingTasks struct {
	creation.Tasks
	code  model.FailureCode
	calls int
}

func (f *failingTasks) Start(_ context.Context, _ string, work creation.WorkItem, _ time.Time) (model.Operation, error) {
	return model.Operation{ID: work.ID, State: model.OperationQueued}, nil
}

func (f *failingTasks) Run(_ context.Context, id, _ string, _ time.Duration, _ time.Time) (model.Operation, error) {
	f.calls++
	return model.Operation{ID: id, State: model.OperationFailed, Attempt: f.calls, FailureCode: f.code, Error: "original failure"}, errors.New("original failure")
}

func TestDriverDoesNotReopenFailuresRequiringIntervention(t *testing.T) {
	for _, code := range []model.FailureCode{model.FailureSubmissionBlocked, model.FailureResultUnknown, ""} {
		t.Run(string(code), func(t *testing.T) {
			st, run := fixture(t)
			goal := goalFunc(func(context.Context, model.CreationRun) (creation.Decision, error) {
				return creation.Decision{Revision: 1, Step: creation.Step{Work: &creation.WorkItem{ID: "write"}}}, nil
			})
			tasks := &failingTasks{code: code}
			driver := creation.New(st, map[model.GoalKind]creation.Goal{run.Goal.Kind: goal}, time.Now)
			outcome, err := driver.Drive(context.Background(), run, tasks, creation.DriveCommand{})
			wantCalls := 1
			if code == "" {
				wantCalls = 4
			}
			if err != nil || outcome.Run.State != model.RunWaitingUser || tasks.calls != wantCalls {
				t.Fatalf("failure policy: calls=%d outcome=%+v err=%v", tasks.calls, outcome, err)
			}
			if !strings.Contains(outcome.Run.StateReason, "original failure") {
				t.Fatalf("diagnostic lost: %+v", outcome.Run)
			}
		})
	}
}

func (g goalFunc) ValidateGoal(json.RawMessage) error { return nil }
func (g goalFunc) Next(ctx context.Context, run model.CreationRun) (creation.Decision, error) {
	return g(ctx, run)
}

func fixture(t *testing.T) (*store.Store, model.CreationRun) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "creation.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	advanceProjectRevision(t, st, 0)
	run, err := st.CreateCreationRun(context.Background(), model.CreationRun{
		ID: "run", ProjectID: "project", Goal: model.CreationRunGoal{Kind: "fixture", Payload: json.RawMessage(`{}`)},
		Strategy: model.CreationRunStrategy{PlanWindowChapters: 1, ReviewCadence: model.ReviewPerPlanWindow},
		Preset:   model.CreationRunPreset{Source: "fixture", Digest: "fixture", Approval: model.ApprovalAuto},
		State:    model.RunRunning, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return st, run
}

func TestDriverRejectsInvalidGoalDecisionBeforeExecuting(t *testing.T) {
	for name, step := range map[string]creation.Step{
		"empty": {}, "ambiguous": {Work: &creation.WorkItem{}, Done: "done"},
	} {
		t.Run(name, func(t *testing.T) {
			st, run := fixture(t)
			goal := goalFunc(func(context.Context, model.CreationRun) (creation.Decision, error) {
				return creation.Decision{Step: step}, nil
			})
			driver := creation.New(st, map[model.GoalKind]creation.Goal{run.Goal.Kind: goal}, time.Now)
			if _, err := driver.Drive(context.Background(), run, nil, creation.DriveCommand{}); !errors.Is(err, model.ErrInvalid) {
				t.Fatalf("invalid decision: %v", err)
			}
			current, err := st.GetCreationRun(context.Background(), run.ID)
			if err != nil || current.State != model.RunRunning {
				t.Fatalf("invalid goal must not settle run: %+v, %v", current, err)
			}
		})
	}
}

func TestDriverPreservesApplicationFailureAndInfrastructureErrors(t *testing.T) {
	cause := errors.New("source could not be read")
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "infrastructure", true: "application"}[failed], func(t *testing.T) {
			st, run := fixture(t)
			goal := goalFunc(func(context.Context, model.CreationRun) (creation.Decision, error) {
				decision := creation.Decision{Revision: 1}
				if failed {
					decision.Step.Fail = "检查证据损坏"
				}
				return decision, cause
			})
			driver := creation.New(st, map[model.GoalKind]creation.Goal{run.Goal.Kind: goal}, time.Now)
			outcome, err := driver.Drive(context.Background(), run, nil, creation.DriveCommand{})
			if !errors.Is(err, cause) {
				t.Fatalf("diagnostic lost: %v", err)
			}
			current, err := st.GetCreationRun(context.Background(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			expected := model.RunRunning
			if failed {
				expected = model.RunFailed
				if outcome.Run.ID != run.ID || outcome.Revision != 1 {
					t.Fatalf("failure outcome: %+v", outcome)
				}
			}
			if current.State != expected {
				t.Fatalf("state = %s, want %s", current.State, expected)
			}
		})
	}
}

func TestDriverHonorsPauseDuringGoalEvaluation(t *testing.T) {
	for name, step := range map[string]creation.Step{
		"work": {Work: &creation.WorkItem{ID: "must-not-start"}}, "complete": {Done: "done"},
	} {
		t.Run(name, func(t *testing.T) {
			st, run := fixture(t)
			goal := goalFunc(func(ctx context.Context, run model.CreationRun) (creation.Decision, error) {
				_, err := st.TransitionCreationRun(ctx, run.ID, model.RunRunning, model.RunPaused, "user pause", 0, run.CreatedAt.Add(time.Second))
				return creation.Decision{Revision: 1, Step: step}, err
			})
			goals := map[model.GoalKind]creation.Goal{run.Goal.Kind: goal}
			driver := creation.New(st, goals, time.Now)
			// Registration is static: changing the caller's map must not alter this driver.
			delete(goals, run.Goal.Kind)
			outcome, err := driver.Drive(context.Background(), run, nil, creation.DriveCommand{})
			if err != nil || outcome.Run.State != model.RunPaused {
				t.Fatalf("user pause lost: %+v, %v", outcome, err)
			}
		})
	}
}

// Persist a real authority revision without coupling the driver tests to an application.
func advanceProjectRevision(t *testing.T, st *store.Store, base model.Revision) {
	t.Helper()
	ctx := context.Background()
	at := time.Date(2026, 9, 10, 12, 0, int(base), 0, time.UTC)
	author := model.Author{Kind: model.AuthorUser, ID: "user"}
	proposal := model.Proposal{
		ID: fmt.Sprintf("edit-%d", base+1), Target: model.AuthorityTarget{Kind: model.AuthorityProject, ID: "project"},
		BaseRevision: base, Author: author, Reason: "edit observed source", ApprovalState: model.ApprovalPending, CreatedAt: at,
		Patches: []model.Patch{{Document: model.DocumentRef{Kind: model.DocumentIntent, ID: "root"}, Operation: model.PatchPut, Content: json.RawMessage(fmt.Sprintf(`{"premise":"source %d","target_chapters":1}`, base+1))}},
	}
	if _, err := st.SaveProposal(ctx, proposal); err != nil {
		t.Fatal(err)
	}
	proposal.ApprovalState, proposal.DecidedBy, proposal.DecidedAt = model.ApprovalApproved, &author, &at
	if _, err := st.CommitProposal(ctx, proposal); err != nil {
		t.Fatal(err)
	}
}

func TestDriverRejectsUnobservedCompletionRevision(t *testing.T) {
	for _, revision := range []model.Revision{0, 2} {
		t.Run(fmt.Sprint(revision), func(t *testing.T) {
			st, run := fixture(t)
			goal := goalFunc(func(context.Context, model.CreationRun) (creation.Decision, error) {
				return creation.Decision{Revision: revision, Step: creation.Step{Done: "done"}}, nil
			})
			driver := creation.New(st, map[model.GoalKind]creation.Goal{run.Goal.Kind: goal}, time.Now)
			if _, err := driver.Drive(context.Background(), run, nil, creation.DriveCommand{}); !errors.Is(err, model.ErrInvalid) {
				t.Fatalf("invalid revision accepted: %v", err)
			}
			current, err := st.GetCreationRun(context.Background(), run.ID)
			if err != nil || current.State != model.RunRunning || current.CompletedRevision != 0 {
				t.Fatalf("invalid completion persisted: %+v, %v", current, err)
			}
		})
	}
}

func TestDriverReevaluatesWhenSourceChangesDuringGoalObservation(t *testing.T) {
	for name, stale := range map[string]creation.Step{
		"complete": {Done: "obsolete completion"}, "wait": {Wait: "obsolete wait"}, "fail": {Fail: "obsolete failure"}, "work": {Work: &creation.WorkItem{ID: "must-not-start"}},
	} {
		t.Run(name, func(t *testing.T) {
			st, run := fixture(t)
			calls := 0
			goal := goalFunc(func(ctx context.Context, run model.CreationRun) (creation.Decision, error) {
				observed, err := st.CurrentRevision(ctx, model.AuthorityTarget{Kind: model.AuthorityProject, ID: run.ProjectID})
				if err != nil {
					return creation.Decision{}, err
				}
				calls++
				if calls == 1 {
					advanceProjectRevision(t, st, observed)
					return creation.Decision{Revision: observed, Step: stale}, nil
				}
				return creation.Decision{Revision: observed, Step: creation.Step{Done: "current completion"}}, nil
			})
			driver := creation.New(st, map[model.GoalKind]creation.Goal{run.Goal.Kind: goal}, time.Now)
			outcome, err := driver.Drive(context.Background(), run, nil, creation.DriveCommand{})
			if err != nil || calls != 2 || outcome.Run.State != model.RunCompleted || outcome.Run.CompletedRevision != 2 || outcome.Run.StateReason != "current completion" {
				t.Fatalf("stale decision advanced run: %+v calls=%d err=%v", outcome, calls, err)
			}
		})
	}
}

func TestDriverReevaluatesWhenGoalOrStrategyChangesDuringObservation(t *testing.T) {
	for _, change := range []string{"goal_payload", "goal_kind", "strategy"} {
		for name, stale := range map[string]creation.Step{"done": {Done: "obsolete completion"}, "work": {Work: &creation.WorkItem{ID: "must-not-start"}}} {
			t.Run(change+"/"+name, func(t *testing.T) {
				st, run := fixture(t)
				calls := 0
				replacementCalls := 0
				expectedGoal := run.Goal
				expectedStrategy := run.Strategy
				fresh := func(observed model.CreationRun) (creation.Decision, error) {
					if !observed.Goal.Equal(expectedGoal) || observed.Strategy != expectedStrategy {
						t.Fatalf("goal did not receive current run: %+v", observed)
					}
					return creation.Decision{Revision: 1, Step: creation.Step{Done: "current completion"}}, nil
				}
				original := goalFunc(func(ctx context.Context, observed model.CreationRun) (creation.Decision, error) {
					calls++
					if calls > 1 {
						if change == "goal_kind" {
							t.Fatal("driver reused previous goal-kind adapter")
						}
						return fresh(observed)
					}
					at := observed.CreatedAt.Add(time.Second)
					switch change {
					case "strategy":
						expectedStrategy.AutoRepairBudget++
						if _, err := st.UpdateCreationRunStrategy(ctx, observed.ID, expectedStrategy, at); err != nil {
							t.Fatal(err)
						}
					default:
						expectedGoal.Payload = json.RawMessage(`{"updated":true}`)
						if change == "goal_kind" {
							expectedGoal.Kind = "replacement"
						}
						if _, err := st.UpdateCreationRunGoal(ctx, observed.ID, expectedGoal, at); err != nil {
							t.Fatal(err)
						}
					}
					return creation.Decision{Revision: 1, Step: stale}, nil
				})
				replacement := goalFunc(func(_ context.Context, observed model.CreationRun) (creation.Decision, error) {
					replacementCalls++
					return fresh(observed)
				})
				driver := creation.New(st, map[model.GoalKind]creation.Goal{run.Goal.Kind: original, "replacement": replacement}, time.Now)
				outcome, err := driver.Drive(context.Background(), run, nil, creation.DriveCommand{})
				if err != nil || outcome.Run.State != model.RunCompleted || outcome.Run.StateReason != "current completion" || calls+replacementCalls != 2 {
					t.Fatalf("obsolete decision advanced run: %+v calls=%d replacement=%d err=%v", outcome, calls, replacementCalls, err)
				}
				if change == "goal_kind" && replacementCalls != 1 {
					t.Fatalf("new goal-kind adapter calls=%d, want 1", replacementCalls)
				}
			})
		}
	}
}

type decisionStore struct {
	creation.Store
	before func()
}

func (s *decisionStore) SettleCreationRun(ctx context.Context, observed model.CreationRun, revision model.Revision, to model.CreationRunState, reason string, at time.Time) (model.CreationRun, error) {
	if s.before != nil {
		before := s.before
		s.before = nil
		before()
	}
	return s.Store.SettleCreationRun(ctx, observed, revision, to, reason, at)
}

func TestDriverReevaluatesWhenObservationChangesAtCommit(t *testing.T) {
	for _, change := range []string{"goal", "strategy", "source", "pause"} {
		t.Run(change, func(t *testing.T) {
			st, run := fixture(t)
			wrapped := &decisionStore{Store: st, before: func() {
				ctx := context.Background()
				var err error
				switch change {
				case "goal":
					_, err = st.UpdateCreationRunGoal(ctx, run.ID, model.CreationRunGoal{Kind: run.Goal.Kind, Payload: json.RawMessage(`{"target":200}`)}, time.Now())
				case "strategy":
					strategy := run.Strategy
					strategy.AutoRepairBudget++
					_, err = st.UpdateCreationRunStrategy(ctx, run.ID, strategy, time.Now())
				case "source":
					advanceProjectRevision(t, st, 1)
				case "pause":
					_, err = st.TransitionCreationRun(ctx, run.ID, model.RunRunning, model.RunPaused, "user pause", 0, time.Now())
				}
				if err != nil {
					t.Fatal(err)
				}
			}}
			calls := 0
			goal := goalFunc(func(ctx context.Context, observed model.CreationRun) (creation.Decision, error) {
				calls++
				revision, err := st.CurrentRevision(ctx, model.AuthorityTarget{Kind: model.AuthorityProject, ID: observed.ProjectID})
				reason := "old goal complete"
				if calls > 1 {
					reason = "current goal complete"
				}
				return creation.Decision{Revision: revision, Step: creation.Step{Done: reason}}, err
			})
			driver := creation.New(wrapped, map[model.GoalKind]creation.Goal{run.Goal.Kind: goal}, time.Now)
			outcome, err := driver.Drive(context.Background(), run, nil, creation.DriveCommand{})
			if err != nil {
				t.Fatal(err)
			}
			if change == "pause" {
				if outcome.Run.State != model.RunPaused || calls != 1 {
					t.Fatalf("pause lost: %+v calls=%d", outcome, calls)
				}
			} else if outcome.Run.State != model.RunCompleted || calls != 2 || outcome.Run.StateReason != "current goal complete" {
				t.Fatalf("obsolete decision committed: %+v calls=%d", outcome, calls)
			}
		})
	}
}

func TestDriverFailureReevaluatesChangedGoal(t *testing.T) {
	st, run := fixture(t)
	calls := 0
	goal := goalFunc(func(ctx context.Context, observed model.CreationRun) (creation.Decision, error) {
		calls++
		if calls == 1 {
			updated := observed.Goal
			updated.Payload = json.RawMessage(`{"fixed":true}`)
			if _, err := st.UpdateCreationRunGoal(ctx, observed.ID, updated, time.Now()); err != nil {
				t.Fatal(err)
			}
			return creation.Decision{Revision: 1, Step: creation.Step{Fail: "obsolete diagnostic"}}, errors.New("old goal error")
		}
		return creation.Decision{Revision: 1, Step: creation.Step{Done: "current goal complete"}}, nil
	})
	driver := creation.New(st, map[model.GoalKind]creation.Goal{run.Goal.Kind: goal}, time.Now)
	outcome, err := driver.Drive(context.Background(), run, nil, creation.DriveCommand{})
	if err != nil || calls != 2 || outcome.Run.State != model.RunCompleted {
		t.Fatalf("obsolete failure settled updated goal: calls=%d state=%s err=%v", calls, outcome.Run.State, err)
	}
}

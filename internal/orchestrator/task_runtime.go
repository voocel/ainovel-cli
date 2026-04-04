package orchestrator

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

type taskLocation struct {
	Chapter int
	Volume  int
	Arc     int
}

type novelTaskRuntime struct {
	store *storepkg.Store
	mu    sync.Mutex
	tasks []domain.TaskRecord
}

const (
	maxRetainedTerminalTasks = 80
	terminalPruneThreshold   = 120
)

func newNovelTaskRuntime(store *storepkg.Store) (*novelTaskRuntime, error) {
	tasks, err := store.Tasks.Load()
	if err != nil {
		return nil, err
	}
	rt := &novelTaskRuntime{
		store: store,
		tasks: append([]domain.TaskRecord(nil), tasks...),
	}
	rt.requeueRunningLocked()
	if err := rt.saveLocked(); err != nil {
		return nil, err
	}
	return rt, nil
}

func (rt *novelTaskRuntime) Reset() error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.tasks = nil
	return rt.saveLocked()
}

func (rt *novelTaskRuntime) Snapshot() []domain.TaskRecord {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	out := append([]domain.TaskRecord(nil), rt.tasks...)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

func (rt *novelTaskRuntime) Reconcile(progress *domain.Progress) error {
	if progress == nil {
		return nil
	}

	rt.mu.Lock()
	defer rt.mu.Unlock()

	completed := make(map[int]struct{}, len(progress.CompletedChapters))
	for _, ch := range progress.CompletedChapters {
		if ch > 0 {
			completed[ch] = struct{}{}
		}
	}

	changed := false
	now := time.Now()
	for i := range rt.tasks {
		task := &rt.tasks[i]
		if task.IsTerminal() {
			continue
		}

		switch task.Kind {
		case domain.TaskFoundationPlan:
			if progress.Phase == domain.PhaseWriting || progress.Phase == domain.PhaseComplete {
				task.Status = domain.TaskSucceeded
				task.UpdatedAt = now
				task.EndedAt = now
				changed = true
			}
		case domain.TaskChapterWrite, domain.TaskChapterRewrite, domain.TaskChapterPolish:
			if _, ok := completed[task.Chapter]; ok {
				task.Status = domain.TaskSucceeded
				task.UpdatedAt = now
				task.EndedAt = now
				changed = true
			}
		}
	}

	if !changed {
		return nil
	}
	return rt.saveLocked()
}

func (rt *novelTaskRuntime) Queue(kind domain.TaskKind, owner, title, input string, loc taskLocation) (domain.TaskRecord, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	if existing := rt.findActiveLocked(kind, owner, loc); existing != nil {
		return *existing, nil
	}
	now := time.Now()
	task := domain.TaskRecord{
		ID:        fmt.Sprintf("task-%d", now.UnixNano()),
		Kind:      kind,
		Owner:     owner,
		Title:     title,
		Status:    domain.TaskQueued,
		Chapter:   loc.Chapter,
		Volume:    loc.Volume,
		Arc:       loc.Arc,
		Input:     strings.TrimSpace(input),
		CreatedAt: now,
		UpdatedAt: now,
	}
	rt.tasks = append(rt.tasks, task)
	return task, rt.saveLocked()
}

func (rt *novelTaskRuntime) Start(kind domain.TaskKind, owner, title, input string, loc taskLocation) (domain.TaskRecord, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	now := time.Now()
	if existing := rt.findActiveLocked(kind, owner, loc); existing != nil {
		existing.Status = domain.TaskRunning
		existing.Title = title
		existing.Input = strings.TrimSpace(input)
		if existing.StartedAt.IsZero() {
			existing.StartedAt = now
		}
		existing.UpdatedAt = now
		return *existing, rt.saveLocked()
	}

	rt.cancelActiveOwnerLocked(owner, now)
	task := domain.TaskRecord{
		ID:        fmt.Sprintf("task-%d", now.UnixNano()),
		Kind:      kind,
		Owner:     owner,
		Title:     title,
		Status:    domain.TaskRunning,
		Chapter:   loc.Chapter,
		Volume:    loc.Volume,
		Arc:       loc.Arc,
		Input:     strings.TrimSpace(input),
		CreatedAt: now,
		StartedAt: now,
		UpdatedAt: now,
	}
	rt.tasks = append(rt.tasks, task)
	return task, rt.saveLocked()
}

func (rt *novelTaskRuntime) CompleteByID(id, outputRef string) error {
	return rt.finishByID(id, domain.TaskSucceeded, outputRef, "")
}

func (rt *novelTaskRuntime) FailByID(id, message string) error {
	return rt.finishByID(id, domain.TaskFailed, "", message)
}

func (rt *novelTaskRuntime) CancelByID(id, message string) error {
	return rt.finishByID(id, domain.TaskCanceled, "", message)
}

func (rt *novelTaskRuntime) CompleteActive(owner string) error {
	return rt.finishActive(owner, domain.TaskSucceeded, "", "")
}

func (rt *novelTaskRuntime) CompleteActiveWithOutput(owner, outputRef string) error {
	return rt.finishActive(owner, domain.TaskSucceeded, outputRef, "")
}

func (rt *novelTaskRuntime) FailActive(owner, message string) error {
	return rt.finishActive(owner, domain.TaskFailed, "", message)
}

func (rt *novelTaskRuntime) CancelActive(owner, message string) error {
	return rt.finishActive(owner, domain.TaskCanceled, "", message)
}

func (rt *novelTaskRuntime) UpdateProgress(owner string, mutate func(*domain.TaskRecord)) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	task := rt.latestActiveByOwnerLocked(owner)
	if task == nil {
		return nil
	}
	mutate(task)
	task.UpdatedAt = time.Now()
	return rt.saveLocked()
}

func (rt *novelTaskRuntime) AttachOutputRef(owner, outputRef string) error {
	outputRef = strings.TrimSpace(outputRef)
	if outputRef == "" {
		return nil
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for i := len(rt.tasks) - 1; i >= 0; i-- {
		task := &rt.tasks[i]
		if task.Owner != owner {
			continue
		}
		task.OutputRef = outputRef
		task.UpdatedAt = time.Now()
		return rt.saveLocked()
	}
	return nil
}

func (rt *novelTaskRuntime) ClearQueued(kind domain.TaskKind, chapter int) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	filtered := rt.tasks[:0]
	for _, task := range rt.tasks {
		if task.Kind == kind && task.Status == domain.TaskQueued && (chapter == 0 || task.Chapter == chapter) {
			continue
		}
		filtered = append(filtered, task)
	}
	rt.tasks = append([]domain.TaskRecord(nil), filtered...)
	return rt.saveLocked()
}

func (rt *novelTaskRuntime) findActiveLocked(kind domain.TaskKind, owner string, loc taskLocation) *domain.TaskRecord {
	for i := len(rt.tasks) - 1; i >= 0; i-- {
		task := &rt.tasks[i]
		if task.Owner != owner || task.Kind != kind || task.IsTerminal() {
			continue
		}
		if task.Chapter != loc.Chapter || task.Volume != loc.Volume || task.Arc != loc.Arc {
			continue
		}
		return task
	}
	return nil
}

func (rt *novelTaskRuntime) latestActiveByOwnerLocked(owner string) *domain.TaskRecord {
	for i := len(rt.tasks) - 1; i >= 0; i-- {
		task := &rt.tasks[i]
		if task.Owner == owner && task.Status == domain.TaskRunning {
			return task
		}
	}
	for i := len(rt.tasks) - 1; i >= 0; i-- {
		task := &rt.tasks[i]
		if task.Owner == owner && !task.IsTerminal() {
			return task
		}
	}
	return nil
}

func (rt *novelTaskRuntime) cancelActiveOwnerLocked(owner string, now time.Time) {
	for i := range rt.tasks {
		task := &rt.tasks[i]
		if task.Owner != owner || task.IsTerminal() {
			continue
		}
		task.Status = domain.TaskCanceled
		task.Error = "任务被后续任务替换"
		task.UpdatedAt = now
		task.EndedAt = now
	}
}

func (rt *novelTaskRuntime) finishByID(id string, status domain.TaskStatus, outputRef, message string) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for i := range rt.tasks {
		task := &rt.tasks[i]
		if task.ID != id {
			continue
		}
		now := time.Now()
		task.Status = status
		task.OutputRef = strings.TrimSpace(outputRef)
		task.Error = strings.TrimSpace(message)
		task.UpdatedAt = now
		task.EndedAt = now
		return rt.saveLocked()
	}
	return nil
}

func (rt *novelTaskRuntime) finishActive(owner string, status domain.TaskStatus, outputRef, message string) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	task := rt.latestActiveByOwnerLocked(owner)
	if task == nil {
		return nil
	}
	now := time.Now()
	task.Status = status
	task.OutputRef = strings.TrimSpace(outputRef)
	task.Error = strings.TrimSpace(message)
	task.UpdatedAt = now
	task.EndedAt = now
	return rt.saveLocked()
}

func (rt *novelTaskRuntime) requeueRunningLocked() {
	now := time.Now()
	for i := range rt.tasks {
		task := &rt.tasks[i]
		if task.Status != domain.TaskRunning {
			continue
		}
		task.Status = domain.TaskQueued
		task.Progress.Stage = "requeued"
		task.Progress.Summary = "检测到上次运行中断，任务已重新排队"
		task.UpdatedAt = now
		task.EndedAt = time.Time{}
	}
}

func (rt *novelTaskRuntime) saveLocked() error {
	rt.compactLocked()
	return rt.store.Tasks.Save(rt.tasks)
}

func (rt *novelTaskRuntime) compactLocked() {
	terminalCount := 0
	for _, task := range rt.tasks {
		if task.IsTerminal() {
			terminalCount++
		}
	}
	if terminalCount <= terminalPruneThreshold {
		return
	}

	keepTerminal := terminalCount
	if keepTerminal > maxRetainedTerminalTasks {
		keepTerminal = maxRetainedTerminalTasks
	}

	keep := make([]bool, len(rt.tasks))
	keptTerminal := 0
	for i := len(rt.tasks) - 1; i >= 0; i-- {
		task := rt.tasks[i]
		if !task.IsTerminal() {
			keep[i] = true
			continue
		}
		if keptTerminal < keepTerminal {
			keep[i] = true
			keptTerminal++
		}
	}

	filtered := rt.tasks[:0]
	for i, ok := range keep {
		if ok {
			filtered = append(filtered, rt.tasks[i])
		}
	}
	rt.tasks = append([]domain.TaskRecord(nil), filtered...)
}

func inferTaskKind(agentName string, progress *domain.Progress, taskText string) domain.TaskKind {
	taskText = strings.TrimSpace(taskText)
	switch {
	case strings.HasPrefix(agentName, "architect"):
		switch {
		case strings.Contains(taskText, "下一卷"):
			return domain.TaskVolumeAppend
		case strings.Contains(taskText, "展开") && strings.Contains(taskText, "弧"):
			return domain.TaskArcExpand
		default:
			return domain.TaskFoundationPlan
		}
	case agentName == "writer":
		if progress != nil {
			switch progress.Flow {
			case domain.FlowRewriting:
				return domain.TaskChapterRewrite
			case domain.FlowPolishing:
				return domain.TaskChapterPolish
			}
		}
		return domain.TaskChapterWrite
	case agentName == "editor":
		return domain.TaskChapterReview
	default:
		return domain.TaskCoordinatorDecision
	}
}

func inferTaskLocation(kind domain.TaskKind, progress *domain.Progress) taskLocation {
	if progress == nil {
		return taskLocation{}
	}
	loc := taskLocation{
		Volume: progress.CurrentVolume,
		Arc:    progress.CurrentArc,
	}
	switch kind {
	case domain.TaskChapterRewrite, domain.TaskChapterPolish:
		if len(progress.PendingRewrites) > 0 {
			loc.Chapter = progress.PendingRewrites[0]
		}
	case domain.TaskChapterReview:
		loc.Chapter = progress.CurrentChapter - 1
	default:
		if progress.InProgressChapter > 0 {
			loc.Chapter = progress.InProgressChapter
		} else {
			loc.Chapter = progress.NextChapter()
		}
	}
	return loc
}

type subagentInvocation struct {
	Agent string `json:"agent"`
	Task  string `json:"task"`
}

func parseSubagentInvocation(args json.RawMessage) (subagentInvocation, bool) {
	var inv subagentInvocation
	if len(args) == 0 {
		return inv, false
	}
	if err := json.Unmarshal(args, &inv); err != nil {
		return inv, false
	}
	if inv.Agent == "" || strings.TrimSpace(inv.Task) == "" {
		return inv, false
	}
	return inv, true
}

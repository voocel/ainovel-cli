package chief

import (
	"context"
	"fmt"
	"time"

	"github.com/voocel/ainovel-cli/internal/app/novel"
	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// WriteParams 是一次推进的篇幅设定：Chapters 正数固定章数，0 交给 AI，novel.KeepChapters
// 沿用；Extend 是完本后续写。Intent 非空时先以它建书（向导完善设定），Premise 与 Approval
// 随建书给出。
type WriteParams struct {
	Chapters int
	Extend   bool
	Premise  string
	Approval model.ApprovalPolicy
	Intent   *model.Intent
}

// DriveEnd 是一次推进的落点：推进到完成、需要等待，或被打断、出错。
type DriveEnd struct {
	Result novel.QuickWriteResult
	Err    error
}

// Production 是这本书此刻的推进：Ended 是已结束的推进次数，界面据此认出新的落点。
type Production struct {
	Writing bool
	Ended   int
	Last    DriveEnd
}

func (s *Session) Production() Production {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Production{Writing: s.writing, Ended: s.ended, Last: s.last}
}

// Write 开始推进，直到完成或需要等待；已经在写时不做什么，返回 false。
func (s *Session) Write(params WriteParams) bool {
	s.mu.Lock()
	if s.writing {
		s.mu.Unlock()
		return false
	}
	ctx, cancel := context.WithCancel(s.ctx)
	driven := make(chan struct{})
	s.writing, s.drive, s.driven, s.paused = true, cancel, driven, false
	s.service.work.Add(1)
	s.mu.Unlock()
	s.notify()
	go func() {
		defer s.service.work.Done()
		defer close(driven)
		defer cancel()
		result, err := s.service.write(ctx, s.projectID, s.userID, params)
		s.mu.Lock()
		s.writing, s.drive = false, nil
		s.ended++
		s.last = DriveEnd{Result: result, Err: err}
		s.mu.Unlock()
		s.notify()
	}()
	return true
}

// restart 按新的篇幅重新推进：在途的推进先停下，任务放回队列。停下之后又有推进抢先开始
// （比如用户刚按了回车），就再停一次：这句话是用户最新的意思。
func (s *Session) restart(params WriteParams) {
	for {
		s.stop()
		if s.Write(params) {
			return
		}
	}
}

// Pause 是用户按键暂停：先落盘暂停，再打断在途的模型调用，任务放回队列；说完一句话后
// 接着写。
func (s *Session) Pause(ctx context.Context) error {
	if err := s.pause(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	s.paused = true
	s.mu.Unlock()
	s.notify()
	return nil
}

// pause 落盘暂停当前这一轮创作并打断推进，等推进停下。
func (s *Session) pause(ctx context.Context) error {
	run, ok, err := s.service.runs.ActiveCreationRun(ctx, s.projectID)
	if err != nil {
		return err
	}
	if ok && run.State != model.RunPaused {
		if _, err := s.service.runs.PauseCreationRun(ctx, run.ID, time.Now().UTC()); err != nil {
			return err
		}
	}
	s.stop()
	return nil
}

// stop 打断推进并等它停下：在途任务由引擎放回队列，续跑接着已有的对话与草稿。
func (s *Session) stop() {
	s.mu.Lock()
	drive, driven := s.drive, s.driven
	s.mu.Unlock()
	if drive == nil {
		return
	}
	drive()
	<-driven
}

func (s *Service) write(ctx context.Context, projectID, userID string, params WriteParams) (novel.QuickWriteResult, error) {
	if params.Intent != nil {
		if _, err := s.projects.CreateProject(ctx, projectdoc.CreateProjectCommand{
			ProjectID: projectID, ChangeID: projectID + ":create", UserID: userID, Reason: "完善设定后创建作品",
			Draft: projectdoc.ProjectDraft{Intent: *params.Intent, Approval: params.Approval}, CreatedAt: time.Now().UTC(),
		}); err != nil {
			return novel.QuickWriteResult{}, err
		}
		params.Premise = params.Intent.Premise
	}
	command, err := s.command(ctx, projectID, userID, params)
	if err != nil {
		return novel.QuickWriteResult{}, err
	}
	return s.novels.QuickWrite(ctx, command)
}

// command 是一次推进的命令：没给前提时取作品的。
func (s *Service) command(ctx context.Context, projectID, userID string, params WriteParams) (novel.QuickWriteCommand, error) {
	premise := params.Premise
	if premise == "" {
		project, err := s.projects.Project(ctx, projectID, model.InitialRevision)
		if err != nil {
			return novel.QuickWriteCommand{}, fmt.Errorf("read premise: %w", err)
		}
		premise = project.Intent.Premise
	}
	return novel.QuickWriteCommand{
		ProjectID: projectID, UserID: userID, Premise: premise,
		Chapters: params.Chapters, Extend: params.Extend, Approval: params.Approval,
		WorkerID: "desk-" + userID, CreatedAt: time.Now().UTC(),
	}, nil
}

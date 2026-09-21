package headless

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/voocel/ainovel-cli/internal/app/novel"
	"github.com/voocel/ainovel-cli/internal/app/task"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain/model"
)

func runQuick(ctx context.Context, api *bootstrap.App, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "write" {
		return fmt.Errorf("quick 需要 write 子命令")
	}
	flags := newFlags("quick write", stderr)
	projectID := flags.String("project", "", "Project ID")
	userID := flags.String("user", "", "User ID")
	premise := flags.String("premise", "", "一句话创作要求")
	chapters := flags.Int("chapters", 3, "连续创作章节数")
	approval := flags.String("approval", "", "审批预设 auto/milestone/manual；留空沿用进行中的创作")
	workerID := flags.String("worker", "quick-worker", "Worker instance ID")
	lease := flags.Duration("lease", task.DefaultLease, "每个 Operation 的 Worker lease")
	var packIDs, profileRefs stringValues
	flags.Var(&packIDs, "pack", "启用 Pack ID；可重复")
	flags.Var(&profileRefs, "creator-profile", "启用 id@scope Creator Profile；可重复")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *projectID == "" || *userID == "" || *premise == "" {
		return fmt.Errorf("quick write 需要 --project --user --premise")
	}
	profiles, err := parseCreatorProfileRefs(profileRefs)
	if err != nil {
		return err
	}
	result, err := api.Novels.QuickWrite(ctx, novel.QuickWriteCommand{
		ProjectID: *projectID, UserID: *userID, Premise: *premise,
		Chapters: *chapters, Approval: model.ApprovalPolicy(*approval),
		WorkerID: *workerID, LeaseDuration: *lease,
		Packs: packRefs(packIDs), CreatorProfiles: profiles,
		CreatedAt: time.Now().UTC(),
	})
	if err := writeResult(stdout, result, err); err != nil {
		return err
	}
	writeQuickGuidance(ctx, api, result, *userID, stdout)
	return nil
}

// writeQuickGuidance 在等待裁决时给出创作语言的下一步指引（D26/C3）：
// 原始候选与诊断仍可通过列出的命令展开查看。
func writeQuickGuidance(
	ctx context.Context,
	api *bootstrap.App,
	result novel.QuickWriteResult,
	userID string,
	stdout io.Writer,
) {
	if result.RunState != model.RunWaitingUser || result.WaitingOperationID == "" {
		return
	}
	proposal, err := api.Decisions.ProposalForOperation(ctx, result.WaitingOperationID)
	if err != nil {
		return
	}
	fmt.Fprintf(stdout, "\n%s\n", result.RunReason)
	fmt.Fprintf(stdout, "  查看这一稿：ainovel-cli --headless proposal show --id %s\n", proposal.ID)
	fmt.Fprintf(stdout, "  满意就通过：ainovel-cli --headless proposal approve --id %s --user %s，再重新执行本命令继续写\n", proposal.ID, userID)
	fmt.Fprintf(stdout, "  想改就否决：ainovel-cli --headless proposal reject --id %s --user %s --reason \"想调整的方向\"，重写会带上你的反馈\n", proposal.ID, userID)
}

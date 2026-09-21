package headless

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/voocel/ainovel-cli/internal/app/decision"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

func runProposal(ctx context.Context, api *bootstrap.App, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("proposal 需要 show、approve、reject 或 resolve 子命令")
	}
	if args[0] == "resolve" {
		flags := newFlags("proposal resolve", stderr)
		id := flags.String("id", "", "Proposal ID")
		userID := flags.String("user", "", "User ID")
		strategy := flags.String("strategy", "", "rewrite_affected、reinterpret_future 或 abandon")
		runID := flags.String("run", "", "rewrite_affected 所属 Creation Run ID")
		reason := flags.String("reason", "", "裁决反馈；放弃候选时会进入后续重写任务")
		coreVersion := flags.String("core", "core-v1", "Core Protocol 版本")
		var packIDs, profileRefs stringValues
		flags.Var(&packIDs, "pack", "受影响章节重写使用的 Pack ID；可重复")
		flags.Var(&profileRefs, "creator-profile", "受影响章节重写使用的 id@scope Profile；可重复")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" || *userID == "" || *strategy == "" || flags.NArg() != 0 {
			return fmt.Errorf("proposal resolve 需要 --id --user --strategy")
		}
		profiles, err := parseCreatorProfileRefs(profileRefs)
		if err != nil {
			return err
		}
		result, err := api.Decisions.ResolveProposal(ctx, decision.ResolveProposalCommand{
			ProposalID: *id, UserID: *userID, Strategy: *strategy, Reason: *reason,
			RunID: *runID,
			Packs: packRefs(packIDs), CreatorProfiles: profiles, CoreProtocolVersion: *coreVersion,
			CreatedAt: time.Now().UTC(),
		})
		return writeResult(stdout, result, err)
	}
	flags := newFlags("proposal "+args[0], stderr)
	id := flags.String("id", "", "Proposal ID")
	userID := flags.String("user", "", "User ID")
	reason := flags.String("reason", "", "否决理由；会进入后续重写任务")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *id == "" {
		return fmt.Errorf("proposal %s 需要 --id", args[0])
	}
	switch args[0] {
	case "show":
		proposal, err := api.Decisions.Proposal(ctx, *id)
		return writeResult(stdout, proposal, err)
	case "approve":
		if *userID == "" {
			return fmt.Errorf("proposal approve 需要 --user")
		}
		changeSet, err := api.Decisions.Approve(ctx, *id, *userID, time.Now().UTC())
		return writeResult(stdout, changeSet, err)
	case "reject":
		if *userID == "" {
			return fmt.Errorf("proposal reject 需要 --user")
		}
		proposal, err := api.Decisions.Reject(ctx, *id, *userID, *reason, time.Now().UTC())
		return writeResult(stdout, proposal, err)
	default:
		return fmt.Errorf("未知 proposal 子命令 %q", args[0])
	}
}

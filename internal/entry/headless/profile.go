package headless

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/voocel/ainovel-cli/internal/app/resource"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/jsonc"
)

func runProfile(ctx context.Context, api *bootstrap.App, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("profile 需要 save、show、learn、candidates 或 confirm 子命令")
	}
	switch args[0] {
	case "save":
		flags := newFlags("profile save", stderr)
		path := flags.String("file", "", "Creator Profile JSON 文件")
		changeID := flags.String("change", "", "Change ID")
		userID := flags.String("user", "", "User ID")
		reason := flags.String("reason", "", "保存原因")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *path == "" || *changeID == "" || *userID == "" || *reason == "" {
			return fmt.Errorf("profile save 需要 --file --change --user --reason")
		}
		var profile model.CreatorProfile
		if err := jsonc.DecodeFile(*path, &profile); err != nil {
			return err
		}
		changeSet, err := api.Resources.SaveCreatorProfile(ctx, *changeID, *userID, *reason, profile, time.Now().UTC())
		return writeResult(stdout, changeSet, err)
	case "show":
		flags := newFlags("profile show", stderr)
		id := flags.String("id", "", "Profile ID")
		scope := flags.String("scope", "", "Profile scope")
		revisionText := flags.String("revision", "0", "Revision；0 表示最新")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" || *scope == "" {
			return fmt.Errorf("profile show 需要 --id --scope")
		}
		revision, err := parseRevision(*revisionText)
		if err != nil {
			return err
		}
		profile, storedRevision, err := api.Resources.CreatorProfile(ctx, *id, *scope, revision)
		return writeResult(stdout, struct {
			Revision model.Revision       `json:"revision"`
			Profile  model.CreatorProfile `json:"profile"`
		}{Revision: storedRevision, Profile: profile}, err)
	case "learn":
		flags := newFlags("profile learn", stderr)
		id := flags.String("id", "", "Profile ID")
		scope := flags.String("scope", "", "Profile scope")
		candidate := flags.String("candidate", "", "Preference Candidate ID")
		projectID := flags.String("project", "", "来源 Project ID")
		fromText := flags.String("from", "", "修改前 Revision")
		toText := flags.String("to", "", "修改后 Revision")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" || *scope == "" || *candidate == "" || *projectID == "" || *fromText == "" || *toText == "" {
			return fmt.Errorf("profile learn 需要 --id --scope --candidate --project --from --to")
		}
		from, err := parseRevision(*fromText)
		if err != nil {
			return err
		}
		to, err := parseRevision(*toText)
		if err != nil {
			return err
		}
		record, err := api.Resources.LearnPreference(ctx, resource.LearnPreferenceCommand{
			CandidateID: *candidate, ProfileID: *id, Scope: *scope, ProjectID: *projectID,
			FromRevision: from, ToRevision: to, CreatedAt: time.Now().UTC(),
		})
		return writeResult(stdout, record, err)
	case "candidates":
		flags := newFlags("profile candidates", stderr)
		id := flags.String("id", "", "Profile ID")
		scope := flags.String("scope", "", "Profile scope")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" || *scope == "" {
			return fmt.Errorf("profile candidates 需要 --id --scope")
		}
		candidates, err := api.Resources.PreferenceCandidates(ctx, *id, *scope)
		return writeResult(stdout, candidates, err)
	case "confirm":
		flags := newFlags("profile confirm", stderr)
		id := flags.String("id", "", "Profile ID")
		scope := flags.String("scope", "", "Profile scope")
		candidate := flags.String("candidate", "", "Preference Candidate ID")
		changeID := flags.String("change", "", "Change ID")
		userID := flags.String("user", "", "User ID")
		reason := flags.String("reason", "", "确认原因")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" || *scope == "" || *candidate == "" || *changeID == "" || *userID == "" || *reason == "" {
			return fmt.Errorf("profile confirm 需要 --id --scope --candidate --change --user --reason")
		}
		changeSet, err := api.Resources.ConfirmPreferenceCandidate(
			ctx, *changeID, *userID, *reason, *id, *scope, *candidate, time.Now().UTC(),
		)
		return writeResult(stdout, changeSet, err)
	default:
		return fmt.Errorf("未知 profile 子命令 %q", args[0])
	}
}

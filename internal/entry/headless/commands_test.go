package headless

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	projectdoc "github.com/voocel/ainovel-cli/internal/app/project"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/store"
)

// 命令表是唯一出处：Run 的分派、main 的装配深度与帮助文本都从它来，
// 任何一处漏掉新命令都应在这里失败。
func TestCommandTableIsTheSingleSourceOfTruth(t *testing.T) {
	var help bytes.Buffer
	if err := writeHelp(&help); err != nil {
		t.Fatalf("write help: %v", err)
	}
	for _, command := range commands() {
		if command.Name == "" || command.Summary == "" {
			t.Errorf("命令 %#v 缺少名字或说明", command)
		}
		if command.run == nil {
			t.Errorf("命令 %q 没有处理函数", command.Name)
		}
		if found, ok := Lookup(command.Name); !ok || found.Name != command.Name {
			t.Errorf("Lookup(%q) 取不到命令表项", command.Name)
		}
		if !strings.Contains(help.String(), command.Name) {
			t.Errorf("帮助文本缺少命令 %q", command.Name)
		}
		switch command.Storage {
		case StorageNone, StorageReadOnly, StorageFull:
		default:
			t.Errorf("命令 %q 的存储需求 %v 不是三种之一", command.Name, command.Storage)
		}
	}
	if _, ok := Lookup("ghost"); ok {
		t.Error("Lookup 认了不存在的命令")
	}
	if err := Run(context.Background(), nil, []string{"ghost"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil ||
		!strings.Contains(err.Error(), "ghost") {
		t.Errorf("未知命令错误 = %v，应指出命令名", err)
	}
}

// 每个子命令的入口最小验收：缺子动作时报错而不是 panic 或静默成功。
func TestEverySubcommandRejectsMissingAction(t *testing.T) {
	ctx := context.Background()
	authorityStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "ainovel.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer authorityStore.Close()
	api := bootstrap.New(authorityStore, bootstrap.Options{})

	// model 无子动作即“显示当前绑定”，不属于必须报错的一类。
	showsWithoutAction := map[string]bool{"model": true}
	for _, command := range commands() {
		if command.Storage != StorageFull || showsWithoutAction[command.Name] {
			continue // help 不开库；diag 无子动作，另有 diag_test 覆盖
		}
		t.Run(command.Name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := Run(ctx, api, []string{command.Name}, &stdout, &stderr)
			if err == nil {
				t.Fatalf("%s 缺子动作却成功了，stdout=%q", command.Name, stdout.String())
			}
		})
	}
}

// 只读动作走完整链路：空库下列表类命令必须成功并给出结构化输出。
func TestReadOnlyListCommandsSucceedOnEmptyStore(t *testing.T) {
	ctx := context.Background()
	authorityStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "ainovel.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer authorityStore.Close()
	api := bootstrap.New(authorityStore, bootstrap.Options{})
	if _, err := api.Projects.CreateProject(ctx, projectdoc.CreateProjectCommand{
		ProjectID: "book-1", ChangeID: "create-book-1", UserID: "user-1", Reason: "创建作品",
		Draft: projectdoc.ProjectDraft{Intent: model.Intent{Premise: "凡人修仙"}}, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}

	cases := map[string][]string{
		"artifact list":  {"artifact", "list", "--project", "book-1"},
		"directive list": {"project", "directive", "list", "--project", "book-1"},
		"project show":   {"project", "show", "--project", "book-1"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if err := Run(ctx, api, args, &stdout, &stderr); err != nil {
				t.Fatalf("%s = %v, stderr=%q", name, err, stderr.String())
			}
			if stdout.Len() == 0 {
				t.Fatalf("%s 没有输出", name)
			}
		})
	}
}

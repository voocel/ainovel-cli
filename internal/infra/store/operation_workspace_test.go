package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// 全量覆盖不带版本前提（D60）：写入方不依赖旧内容，版本号是宿主已知的事实，
// 让模型猜它只会白撞——线上 81 次工具报错里 14 次是这么来的。
// 按块编辑仍带前提：那时版本来自刚才的读取，不是猜的。
func TestWorkspaceOverwriteNeedsNoVersionButEditStillDoes(t *testing.T) {
	s := openOperationStore(t)
	ctx := context.Background()
	now := operationTime()
	if _, err := s.CreateOperation(ctx, testOperation("op-1", 1, now)); err != nil {
		t.Fatalf("create: %v", err)
	}
	claimed, err := s.ClaimNextOperation(ctx, "worker-1", time.Minute, now.Add(time.Second))
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	put := func(content string, expected *int64) (model.WorkspaceArtifact, error) {
		return s.PutWorkspaceArtifact(ctx, model.WorkspaceArtifact{
			OperationID: claimed.ID, Key: "ch-001", MediaType: "text/markdown",
			Content: []byte(content), UpdatedAt: now,
		}, expected, claimed.Attempt)
	}

	// 连写三次，全程不报版本：每次都在当前版本之上递增。
	for want := int64(1); want <= 3; want++ {
		artifact, err := put("第 N 版", nil)
		if err != nil {
			t.Fatalf("第 %d 次覆盖: %v", want, err)
		}
		if artifact.Version != want {
			t.Fatalf("版本 = %d, want %d", artifact.Version, want)
		}
	}

	// 编辑路径的前提仍然生效，且报错要说清当前版本。
	stale := int64(1)
	_, err = put("按块编辑", &stale)
	if !errors.Is(err, model.ErrWorkspaceConflict) {
		t.Fatalf("过期版本前提 error = %v, want ErrWorkspaceConflict", err)
	}
	if !contains(err.Error(), "at version 3") {
		t.Errorf("冲突信息没给出当前版本，模型无法自纠：%v", err)
	}
	current := int64(3)
	if artifact, err := put("按块编辑", &current); err != nil || artifact.Version != 4 {
		t.Fatalf("正确版本前提应通过：%#v %v", artifact, err)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

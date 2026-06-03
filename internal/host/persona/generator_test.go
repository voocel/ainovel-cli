// internal/host/persona/generator_test.go
package persona

import (
	"context"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/store"
)

func TestGenerate_UsesCacheOnSecondCall(t *testing.T) {
	st := store.NewStore(t.TempDir())
	calls := 0
	gen := func(ctx context.Context, author string) (string, error) {
		calls++
		return "文风:" + author, nil
	}
	g := New(st, gen)

	authors := []string{"乌贼", "土豆"}
	p1, err := g.EnsurePersonas(context.Background(), authors)
	if err != nil {
		t.Fatalf("first EnsurePersonas: %v", err)
	}
	if len(p1) != 2 || calls != 2 {
		t.Fatalf("first call personas=%d calls=%d", len(p1), calls)
	}
	p2, err := g.EnsurePersonas(context.Background(), authors)
	if err != nil {
		t.Fatalf("second EnsurePersonas: %v", err)
	}
	if calls != 2 {
		t.Fatalf("缓存未命中，calls=%d", calls)
	}
	if p2[0].StyleBlock != "文风:乌贼" {
		t.Fatalf("cached style = %q", p2[0].StyleBlock)
	}
}

func TestEnsurePersonas_SlugFollowsCurrentOrder_NotCache(t *testing.T) {
	st := store.NewStore(t.TempDir())
	gen := func(ctx context.Context, author string) (string, error) { return "风格:" + author, nil }
	g := New(st, gen)
	// 第一次:乌贼在 index0 → persona1,土豆 index1 → persona2
	_, _ = g.EnsurePersonas(context.Background(), []string{"乌贼", "土豆"})
	// 第二次:对调顺序。EnsurePersonas 返回的 slug 必须跟随当前 index,而非缓存旧值
	got, err := g.EnsurePersonas(context.Background(), []string{"土豆", "乌贼"})
	if err != nil {
		t.Fatalf("EnsurePersonas: %v", err)
	}
	// 关键:EnsurePersonas 返回的 slug 顺序必须与 Slugs() 一致
	want := Slugs([]string{"土豆", "乌贼"}) // [persona1, persona2]
	for i := range want {
		if got[i].Slug != want[i] {
			t.Fatalf("位置 %d: EnsurePersonas slug=%q, Slugs()=%q — 缓存命中后 slug 未跟随当前 index", i, got[i].Slug, want[i])
		}
		if got[i].Author != []string{"土豆", "乌贼"}[i] {
			t.Fatalf("位置 %d author 错位: %q", i, got[i].Author)
		}
	}
}

func TestGenerate_FallbackOnError(t *testing.T) {
	st := store.NewStore(t.TempDir())
	gen := func(ctx context.Context, author string) (string, error) {
		return "", context.DeadlineExceeded
	}
	g := New(st, gen)
	got, err := g.EnsurePersonas(context.Background(), []string{"乌贼"})
	if err != nil {
		t.Fatalf("生成失败应兜底而非报错: %v", err)
	}
	if !got[0].Fallback || got[0].StyleBlock == "" {
		t.Fatalf("应使用兜底文案, got %+v", got[0])
	}
}

func TestSlugs_ChineseAndASCII(t *testing.T) {
	// 中文作者名 → personaN 序号；ASCII → 小写
	got := Slugs([]string{"乌贼", "Brandon Sanderson"})
	if got[0] != "persona1" {
		t.Fatalf("中文 slug = %q, want persona1", got[0])
	}
	if got[1] != "brandon-sanderson" {
		t.Fatalf("ASCII slug = %q, want brandon-sanderson", got[1])
	}
}

func TestSlugs_FiltersPathUnsafeChars(t *testing.T) {
	got := Slugs([]string{"J.R.R. Tolkien", "a/b\\c"})
	for _, s := range got {
		for _, bad := range []string{".", "/", "\\"} {
			if strings.Contains(s, bad) {
				t.Fatalf("slug %q 含路径不安全字符 %q", s, bad)
			}
		}
	}
}

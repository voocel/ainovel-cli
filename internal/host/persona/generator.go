// internal/host/persona/generator.go
package persona

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// StyleGenFunc 依作者名生成文风 prompt 片段。注入以便测试与解耦具体 LLM。
type StyleGenFunc func(ctx context.Context, author string) (string, error)

// Generator 负责生成并缓存写作人格。
type Generator struct {
	store *store.Store
	gen   StyleGenFunc
}

// New 创建 Generator，注入 store 与文风生成函数。
func New(store *store.Store, gen StyleGenFunc) *Generator {
	return &Generator{store: store, gen: gen}
}

// EnsurePersonas 返回与 authors 对应的人格列表：命中缓存直接返回，
// 缺失的逐个生成（失败用兜底文案），最后整体写回缓存。
func (g *Generator) EnsurePersonas(ctx context.Context, authors []string) ([]domain.Persona, error) {
	// 读取已有缓存，不存在则得到空 map
	// 错误被忽略：personas.json 损坏时静默重建（重新生成比阻断流程更合适）
	cached, _ := g.store.Contest.LoadPersonas()
	if cached == nil {
		cached = make(map[string]domain.Persona)
	}
	out := make([]domain.Persona, 0, len(authors))
	dirty := false

	for i, author := range authors {
		// 缓存命中且 StyleBlock 非空时复用文风，但 slug 必须按当前 index 重算。
		// 缓存的价值在于不重新调 LLM 生成 StyleBlock；slug 是 index 相关的（中文作者
		// → persona{index+1}），若沿用缓存里的旧 slug，重排序/前插 persona 会导致
		// slug 与 Slugs() 映射颠倒（build.go 注册与 host.go 路由张冠李戴 → 文风错位）。
		if p, ok := cached[author]; ok && p.StyleBlock != "" {
			p.Slug = slugFor(author, i)
			out = append(out, p)
			continue
		}
		// 缓存未命中，调用生成函数
		slug := slugFor(author, i)
		style, err := g.gen(ctx, author)
		p := domain.Persona{Slug: slug, Author: author, StyleBlock: style}
		if err != nil || style == "" {
			// 生成失败时使用通用兜底文案，不阻断流程
			p.StyleBlock = fmt.Sprintf("请尽量模仿网文作者「%s」的文风进行创作：在句式节奏、用词习惯、叙事视角与情绪渲染上贴近其代表作的特征。", author)
			p.Fallback = true
		}
		out = append(out, p)
		cached[author] = p
		dirty = true
	}

	// 有新生成项时写回缓存
	if dirty {
		if err := g.store.Contest.SavePersonas(cached); err != nil {
			return out, fmt.Errorf("cache personas: %w", err)
		}
	}
	return out, nil
}

// slugFor 生成稳定 slug：纯 ASCII 作者名转小写（空格转连字符），
// 含非 ASCII（中文等）则回退 persona{序号}，保证唯一稳定。
func slugFor(author string, index int) string {
	ascii := true
	for _, r := range author {
		if r > unicode.MaxASCII {
			ascii = false
			break
		}
	}
	if !ascii {
		return fmt.Sprintf("persona%d", index+1)
	}
	// 非字母数字一律转连字符，折叠连续连字符并去除首尾，避免污染文件路径
	out := make([]rune, 0, len(author))
	prevHyphen := false
	for _, r := range author {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			out = append(out, unicode.ToLower(r))
			prevHyphen = false
		} else if !prevHyphen {
			out = append(out, '-')
			prevHyphen = true
		}
	}
	slug := strings.Trim(string(out), "-")
	if slug == "" {
		slug = fmt.Sprintf("persona%d", index+1) // 全特殊字符兜底
	}
	return slug
}

// Slugs 把作者名列表转为稳定 slug 列表（与 EnsurePersonas 一致）。
// Task 12 (build.go/host.go) 使用此函数推导 agent 命名，必须与 EnsurePersonas 完全一致。
func Slugs(authors []string) []string {
	out := make([]string, len(authors))
	for i, a := range authors {
		out[i] = slugFor(a, i)
	}
	return out
}

package skills

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Store 管理全局 skill 库（默认 ~/.ainovel/skills/<category>/<name>.md）。
//
// 设计：
//   - 启动时调用 Refresh() 扫描磁盘，构建内存索引（仅元数据，不含正文）
//   - 写入（Add/Remove）后自动 Refresh，保证索引一致
//   - 用户外部编辑文件时，下次 Search/List 前按 mtime 懒重载
//   - 解析失败的文件被跳过（best-effort），不影响其他 skill 可用
//
// 并发：所有读操作 RLock、写操作 Lock，工具调用安全。
type Store struct {
	root     string
	mu       sync.RWMutex
	index    []SkillMeta
	lastScan time.Time
}

// NewStore 创建 store。root 为空时，所有操作返回空结果（不报错）——
// 用于禁用 skill 库的场景（如配置错误时优雅降级）。
func NewStore(root string) *Store {
	return &Store{root: root}
}

// Root 返回根目录路径（CLI 展示用）。
func (s *Store) Root() string { return s.root }

// Refresh 扫描 root 重建索引。root 不存在时自动创建空目录。
// 解析失败的文件被跳过（不影响整体），返回的 error 仅代表结构性问题（如目录无法创建）。
func (s *Store) Refresh() error {
	if s.root == "" {
		return nil
	}
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return fmt.Errorf("create skills dir: %w", err)
	}

	var index []SkillMeta
	_ = filepath.WalkDir(s.root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // 访问失败：跳过此路径但不中止
		}
		if d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		meta, _, err := ParseSkill(path, string(raw))
		if err != nil {
			return nil // 解析失败：跳过单文件，不影响整体
		}
		// 仅当 frontmatter 缺失时 meta.Name 为空——也跳过
		if meta.Name == "" {
			return nil
		}
		index = append(index, meta)
		return nil
	})

	s.mu.Lock()
	s.index = index
	s.lastScan = time.Now()
	s.mu.Unlock()
	return nil
}

// maybeRefresh 比较 root 的 mtime 与 lastScan，变化则重载。
// 避免每次工具调用都全扫，但保证用户手动改文件能被感知。
// 失败静默：最坏情况是读到旧索引，下次工具调用再试。
func (s *Store) maybeRefresh() {
	if s.root == "" {
		return
	}
	info, err := os.Stat(s.root)
	if err != nil {
		return
	}
	s.mu.RLock()
	last := s.lastScan
	s.mu.RUnlock()
	if info.ModTime().After(last) {
		_ = s.Refresh()
	}
}

// Search 按加权评分检索 skill。limit<=0 时取默认 5。
//
// 评分规则（query 非空时）：
//   - tag 精确命中（不分大小写）：+30
//   - trigger 部分匹配（query 包含或被包含）：+25
//   - description 子串匹配：+15
//   - name 子串匹配：+10
//   - priority 仅在已命中时作微调加分：+ priority / 10
//
// query 为空：仅按 priority 排序返回前 limit 条（用于"列概览"场景）。
//
// 重要：query 非空时，必须 matchScore > 0 才算命中——priority 不会让不相关的
// skill 出现在结果里。
func (s *Store) Search(query string, limit int) []SkillMeta {
	if limit <= 0 {
		limit = 5
	}
	s.maybeRefresh()

	s.mu.RLock()
	defer s.mu.RUnlock()

	q := strings.ToLower(strings.TrimSpace(query))
	type scored struct {
		meta  SkillMeta
		score int
	}
	hits := make([]scored, 0, len(s.index))
	for _, m := range s.index {
		if q == "" {
			score := m.Priority
			if score > 0 {
				hits = append(hits, scored{m, score})
			}
			continue
		}
		matchScore := 0
		if containsCI(m.Tags, q) {
			matchScore += 30
		}
		for _, t := range m.Triggers {
			tl := strings.ToLower(t)
			if strings.Contains(tl, q) || strings.Contains(q, tl) {
				matchScore += 25
				break
			}
		}
		if strings.Contains(strings.ToLower(m.Description), q) {
			matchScore += 15
		}
		if strings.Contains(strings.ToLower(m.Name), q) {
			matchScore += 10
		}
		if matchScore > 0 {
			hits = append(hits, scored{m, matchScore + m.Priority/10})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].meta.Name < hits[j].meta.Name
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]SkillMeta, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.meta)
	}
	return out
}

// Read 返回某 skill 的完整文件内容（含 frontmatter）。
// 不存在时返回错误（让 LLM 知道 name 错了）。
func (s *Store) Read(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("skill name is empty")
	}
	s.maybeRefresh()
	s.mu.RLock()
	path := ""
	for _, m := range s.index {
		if m.Name == name {
			path = m.Path
			break
		}
	}
	s.mu.RUnlock()
	if path == "" {
		return "", fmt.Errorf("skill %q not found", name)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read skill %s: %w", name, err)
	}
	return string(data), nil
}

// Add 写入新 skill 文件，frontmatter 由 caller 提供。
//   - name 必须为 [a-z0-9-]+
//   - category 缺省 "misc"，必须为 [a-z0-9-]+
//   - 同名 skill 已存在 → 报错（不覆盖用户手改）
//   - 写入成功后自动 Refresh 索引
func (s *Store) Add(meta SkillMeta, body string) error {
	name := strings.TrimSpace(meta.Name)
	if name == "" {
		return fmt.Errorf("skill name is required")
	}
	if !isValidName(name) {
		return fmt.Errorf("skill name must match [a-z0-9-]+, got %q", name)
	}
	category := strings.TrimSpace(meta.Category)
	if category == "" {
		category = "misc"
	}
	if !isValidName(category) {
		return fmt.Errorf("category must match [a-z0-9-]+, got %q", category)
	}
	if s.root == "" {
		return fmt.Errorf("skill store root is empty (disabled)")
	}

	dir := filepath.Join(s.root, category)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create skill dir: %w", err)
	}
	path := filepath.Join(dir, name+".md")
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("skill %q already exists at %s", name, path)
	}

	content := renderSkillFile(meta, body)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write skill: %w", err)
	}
	return s.Refresh()
}

// Remove 删除 skill。不存在返回错误。删除后自动 Refresh。
func (s *Store) Remove(name string) error {
	if name == "" {
		return fmt.Errorf("skill name is empty")
	}
	s.maybeRefresh()
	s.mu.RLock()
	path := ""
	for _, m := range s.index {
		if m.Name == name {
			path = m.Path
			break
		}
	}
	s.mu.RUnlock()
	if path == "" {
		return fmt.Errorf("skill %q not found", name)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove skill %s: %w", name, err)
	}
	return s.Refresh()
}

// List 返回所有 skill 元数据。category 非空时按分类过滤。
// 返回结果按 name 升序，便于 CLI 列表展示稳定。
func (s *Store) List(category string) []SkillMeta {
	s.maybeRefresh()
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]SkillMeta, 0, len(s.index))
	for _, m := range s.index {
		if category != "" && m.Category != category {
			continue
		}
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Categories 返回所有现存分类（去重、升序）。CLI 列分类用。
func (s *Store) Categories() []string {
	s.maybeRefresh()
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := map[string]struct{}{}
	for _, m := range s.index {
		if m.Category == "" {
			continue
		}
		seen[m.Category] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for c := range seen {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// renderSkillFile 把 meta + body 序列化为 .md 文件全文（frontmatter + body）。
// 写出的 frontmatter 严格符合 ParseSkill 的 YAML 子集，保证 round-trip。
func renderSkillFile(meta SkillMeta, body string) string {
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString("name: " + meta.Name + "\n")
	sb.WriteString("description: " + yamlScalar(meta.Description) + "\n")
	if meta.Category != "" {
		sb.WriteString("category: " + meta.Category + "\n")
	}
	if len(meta.Tags) > 0 {
		sb.WriteString("tags: [" + strings.Join(quoteItems(meta.Tags), ", ") + "]\n")
	}
	if len(meta.Triggers) > 0 {
		sb.WriteString("triggers: [" + strings.Join(quoteItems(meta.Triggers), ", ") + "]\n")
	}
	if strings.TrimSpace(meta.When) != "" {
		sb.WriteString("when: |\n")
		writeBlockScalar(&sb, meta.When)
	}
	if strings.TrimSpace(meta.Do) != "" {
		sb.WriteString("do: |\n")
		writeBlockScalar(&sb, meta.Do)
	}
	if meta.Priority > 0 && meta.Priority != 50 {
		sb.WriteString(fmt.Sprintf("priority: %d\n", meta.Priority))
	}
	sb.WriteString("---\n\n")
	sb.WriteString(strings.TrimSpace(body))
	sb.WriteString("\n")
	return sb.String()
}

// helpers -------------------------------------------------------------------

// containsCI 不区分大小写判断 slice 是否包含 s。
func containsCI(slice []string, s string) bool {
	for _, x := range slice {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

// yamlScalar 把字符串编码为 YAML scalar。
// 含特殊字符（:[]#*!|>'"%@`{} 等）或为空时用双引号 + JSON 转义；否则裸用。
func yamlScalar(s string) string {
	if s == "" {
		return `""`
	}
	if needsYAMLQuote(s) {
		b, _ := json.Marshal(s)
		return string(b)
	}
	return s
}

// needsYAMLQuote 判断字符串是否含 YAML 中有结构含义的字符，
// 含则需用引号包围以免被解析器误解。拆成多次 ContainsAny 是为了
// 避免在双引号字符串字面量里同时转义 " ` { } 引起的视觉混乱。
func needsYAMLQuote(s string) bool {
	if strings.ContainsAny(s, ":[]#*,!|>'\"%@&") {
		return true
	}
	if strings.ContainsAny(s, "{}") {
		return true
	}
	if strings.ContainsRune(s, '`') {
		return true
	}
	if strings.ContainsRune(s, '#') {
		return true
	}
	if strings.ContainsRune(s, '*') {
		return true
	}
	return false
}

func quoteItems(items []string) []string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = yamlScalar(s)
	}
	return out
}

func writeBlockScalar(sb *strings.Builder, text string) {
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			sb.WriteString("\n")
			continue
		}
		sb.WriteString("  " + line + "\n")
	}
}

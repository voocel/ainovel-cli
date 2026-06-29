package rules

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LoadOptions 是 Load 的Nhập参数。
//
// Tập tin不存在不算Lỗi，loader 静默Bỏ qua；解析Thất bại不阻断，conflicts 由 parser 写入 Parsed.Conflicts。
type LoadOptions struct {
	// RulesFS 是 assets/rules 子树。约定根Thư mục直接包含 default.md。
	// 通常通过 fs.Sub(embedFS, "rules") 得到；nil 表示Bỏ qua内置规则。
	RulesFS fs.FS

	// HomeRulesDir 是 ~/.ainovel/rules/ Thư mục；loader 扫描其下所有顶层 .md（Tập tin名字典序合并）。Rỗng表示Bỏ qua。
	HomeRulesDir string

	// ProjectRulesDir 是 ./.ainovel/rules/ Thư mục（镜像全局，同样扫描其下所有顶层 .md）。Rỗng表示Bỏ qua。
	ProjectRulesDir string
}

// Load 按 Default → Global → Project 顺序Đọc，Quay lại升序排好的 Parsed 列表。
//
// merger 接收Quay lại值后只需按列表顺序合并即可，后者覆盖前者。
// 不引入二阶段加载——Genre / Learned 等扩展层在真有内容前不开洞。
func Load(opts LoadOptions) []Parsed {
	var layers []Parsed
	if p, ok := readFromFS(opts.RulesFS, "default.md", SourceDefault, "assets/rules/default.md"); ok {
		layers = append(layers, p)
	}
	layers = append(layers, readDirFromDisk(opts.HomeRulesDir, SourceGlobal)...)
	layers = append(layers, readDirFromDisk(opts.ProjectRulesDir, SourceProject)...)
	return layers
}

// readFromFS 从 fs.FS Đọc并解析；Tập tin不存在Quay lại (Parsed{}, false)。
// displayPath 用于 Parsed.Source（便于在 sources/conflicts 里显示为 "assets/rules/..."）。
func readFromFS(fsys fs.FS, name string, kind SourceKind, displayPath string) (Parsed, bool) {
	if fsys == nil {
		return Parsed{}, false
	}
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		// Tập tin不存在静默Bỏ qua；KhácLỗi也不阻断（loader 设计上不报错）
		if errors.Is(err, fs.ErrNotExist) || os.IsNotExist(err) {
			return Parsed{}, false
		}
		// 极少数 IO Lỗi：作为 parse_error 暴露，避免静默
		return Parsed{
			Source: displayPath,
			Kind:   kind,
			Conflicts: []Conflict{{
				Source: displayPath,
				Kind:   ConflictParseError,
				Detail: "ĐọcThất bại: " + err.Error(),
			}},
		}, true
	}
	return Parse(displayPath, kind, data), true
}

// readFromDisk 从绝对Đường dẫnĐọc并解析；RỗngĐường dẫn或Tập tin不存在Quay lại (Parsed{}, false)。
func readFromDisk(absPath string, kind SourceKind) (Parsed, bool) {
	if strings.TrimSpace(absPath) == "" {
		return Parsed{}, false
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return Parsed{}, false
		}
		return Parsed{
			Source: absPath,
			Kind:   kind,
			Conflicts: []Conflict{{
				Source: absPath,
				Kind:   ConflictParseError,
				Detail: "ĐọcThất bại: " + err.Error(),
			}},
		}, true
	}
	return Parse(absPath, kind, data), true
}

// readDirFromDisk 扫描Thư mục下所有顶层 .md Tập tin（Tập tin名字典序），逐个解析为 Parsed。
// 字典序保证同层多Tập tin的合并顺序稳定、可预期（后者覆盖前者）。
// Bỏ qua子Thư mục与 . 开头的隐藏/Sửa器临时Tập tin（如 macOS ._x.md、emacs .#x.md），
// 避免把脏Tập tin的二进制内容当成偏好Chính văn注入 LLM。
// RỗngĐường dẫn或Thư mục不存在Quay lại nil（静默Bỏ qua，与单Tập tin缺失一致）；
// Thư mục存在但读Thất bại（权限 / Đường dẫn其实是Tập tin）暴露 ConflictParseError，不静默吞错——
// 与 readFromFS / readFromDisk 的容错契约保持一致。
// 不递归子Thư mục——保持扁平，避免引入隐式层级。
func readDirFromDisk(dir string, kind SourceKind) []Parsed {
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return []Parsed{{
			Source: dir,
			Kind:   kind,
			Conflicts: []Conflict{{
				Source: dir,
				Kind:   ConflictParseError,
				Detail: "规则Thư mụcĐọcThất bại: " + err.Error(),
			}},
		}}
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") || !strings.EqualFold(filepath.Ext(e.Name()), ".md") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	var out []Parsed
	for _, name := range names {
		if p, ok := readFromDisk(filepath.Join(dir, name), kind); ok {
			out = append(out, p)
		}
	}
	return out
}

// ainovelDirName 是 ainovel 在 user / project 两级共用的 dotdir 名。
// 全局 ~/.ainovel/rules/ 与项目 ./.ainovel/rules/ 由此对称。
const ainovelDirName = ".ainovel"

// DefaultProjectRulesDir 拼出 ./.ainovel/rules/ 的绝对Đường dẫn（基于给定项目Thư mục）。
// 调用方传入项目根，避免在 loader 内部依赖 cwd；镜像 DefaultHomeRulesDir。
func DefaultProjectRulesDir(projectDir string) string {
	if projectDir == "" {
		return ""
	}
	return filepath.Join(projectDir, ainovelDirName, "rules")
}

// DefaultHomeRulesDir 拼出 ~/.ainovel/rules/ Thư mục的绝对Đường dẫn。
// home 解析Thất bạiQuay lạiRỗng串（调用方据此Bỏ qua该来源）。
func DefaultHomeRulesDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ainovelDirName, "rules")
}

// homeRulesReadme 是首次引导时写入 ~/.ainovel/rules/README.txt 的说明。
// 刻意用 .txt 后缀而非 .md——loader 只扫描 .md，这份说明不会被当成规则注入 LLM。
const homeRulesReadme = `这里放全局Viết偏好，跨所有书生效。

最简单：Mới建一个 .md Tập tin（如 my-style.md），用大白话写偏好就行——
不Cần任何格式、不Cần YAML：

    # 角色
    - 主角林尘别写成圣母，外冷内热即可
    # 风格
    - 多用身体感知（指节发白）替代情绪标签（紧张）
    - 对话别太书面

这些会原样交给 editor 按语义审阅。多个 .md 按Tập tin名字典序合并；
点开头的隐藏Tập tin、非 .md Tập tin都会被忽略（所以这份 README.txt 不会被当成规则）。

进阶（可选）：想要"字数 / 禁词"这类硬性、确定的机械Kiểm tra，
可在Tập tin顶部加一段 YAML front matter——commit_chapter 会逐字计数、强制报错：

    ---
    chapter_words: 3000-6000          # Chương字数范围
    forbidden_phrases: ["某种程度上"]  # Tắt短语，出现即报错
    fatigue_words: {不禁: 1}           # 疲劳词，每章超阈值告警
    ---
    （下面照常写大白话偏好）

不写也没关系：常见 AI 套句、疲劳词的机械基线已内置，开箱即用。

加载优先级（高 → 低）：./.ainovel/rules/*.md（本书） > ~/.ainovel/rules/*.md（这里） > 内置Mặc định
`

// EnsureHomeRulesDir 尽力Tạo ~/.ainovel/rules/ Thư mục并写入 README.txt 引导，
// 让用户发现这个全局偏好扩展点、知道怎么写。
// nice-to-have，非关键Đường dẫn：home 解析Thất bại或写入出错都静默吞掉，绝不阻断启动。
func EnsureHomeRulesDir() {
	if dir := DefaultHomeRulesDir(); dir != "" {
		_ = ensureRulesDirAt(dir)
	}
}

// ensureRulesDirAt TạoThư mục并把 README.txt 写成Hiện tại引导模板，是 EnsureHomeRulesDir 的可测内核。
// README.txt 是系统生成的引导Tập tin（用户偏好写在 *.md，它不被 loader 加载），每次都覆盖为
// 最Mới模板——不保留Cũ内容，也就不Cần任何Phiên bản兼容逻辑。
func ensureRulesDirAt(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "README.txt"), []byte(homeRulesReadme), 0o644)
}

// DefaultOptions 根据Hiện tại工作Thư mục构造常用 LoadOptions。
//
// 适合 Host 启动时调用一次，让 ContextTool / CommitChapterTool 复用同一份Cấu hình。
// 解析 cwd Thất bại时 ProjectRulesDir 留Rỗng（loader 会Bỏ qua该来源）。
//
// Đường dẫn语义：ProjectRulesDir 绑定 **Hiện tại工作Thư mục（cwd）** 而非 outputDir。
// 用户 cd 到不同Thư mục启动写不同的书，./.ainovel/rules/ 自然跟着 cwd 走；如需跨书共享，
// 放 ~/.ainovel/rules/ 全局Thư mục即可（其下所有 .md 都会被加载）。
func DefaultOptions(rulesFS fs.FS) LoadOptions {
	cwd, _ := os.Getwd()
	return LoadOptions{
		RulesFS:         rulesFS,
		HomeRulesDir:    DefaultHomeRulesDir(),
		ProjectRulesDir: DefaultProjectRulesDir(cwd),
	}
}

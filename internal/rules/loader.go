package rules

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LoadOptions 是 Load 的输入参数。
//
// 文件不存在不算错误，loader 静默跳过；解析失败不阻断，conflicts 由 parser 写入 Parsed.Conflicts。
type LoadOptions struct {
	// RulesFS 是 assets/rules 子树。约定根目录直接包含 default.md。
	// 通常通过 fs.Sub(embedFS, "rules") 得到；nil 表示跳过内置规则。
	RulesFS fs.FS

	// HomeRulesDir 是 ~/.ainovel/rules/ 目录；loader 扫描其下所有顶层 .md（文件名字典序合并）。空表示跳过。
	HomeRulesDir string

	// ProjectRulesDir 是 ./.ainovel/rules/ 目录（镜像全局，同样扫描其下所有顶层 .md）。空表示跳过。
	ProjectRulesDir string
}

// Load 按 Default → Global → Project 顺序读取，返回升序排好的 Parsed 列表。
//
// merger 接收返回值后只需按列表顺序合并即可，后者覆盖前者。
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

// readFromFS 从 fs.FS 读取并解析；文件不存在返回 (Parsed{}, false)。
// displayPath 用于 Parsed.Source（便于在 sources/conflicts 里显示为 "assets/rules/..."）。
func readFromFS(fsys fs.FS, name string, kind SourceKind, displayPath string) (Parsed, bool) {
	if fsys == nil {
		return Parsed{}, false
	}
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		// 文件不存在静默跳过；其他错误也不阻断（loader 设计上不报错）
		if errors.Is(err, fs.ErrNotExist) || os.IsNotExist(err) {
			return Parsed{}, false
		}
		// 极少数 IO 错误：作为 parse_error 暴露，避免静默
		return Parsed{
			Source: displayPath,
			Kind:   kind,
			Conflicts: []Conflict{{
				Source: displayPath,
				Kind:   ConflictParseError,
				Detail: "读取失败: " + err.Error(),
			}},
		}, true
	}
	return Parse(displayPath, kind, data), true
}

// readFromDisk 从绝对路径读取并解析；空路径或文件不存在返回 (Parsed{}, false)。
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
				Detail: "读取失败: " + err.Error(),
			}},
		}, true
	}
	return Parse(absPath, kind, data), true
}

// readDirFromDisk 扫描目录下所有顶层 .md 文件（文件名字典序），逐个解析为 Parsed。
// 字典序保证同层多文件的合并顺序稳定、可预期（后者覆盖前者）。
// 跳过子目录与 . 开头的隐藏/编辑器临时文件（如 macOS ._x.md、emacs .#x.md），
// 避免把脏文件的二进制内容当成偏好正文注入 LLM。
// 空路径或目录不存在返回 nil（静默跳过，与单文件缺失一致）；
// 目录存在但读失败（权限 / 路径其实是文件）暴露 ConflictParseError，不静默吞错——
// 与 readFromFS / readFromDisk 的容错契约保持一致。
// 不递归子目录——保持扁平，避免引入隐式层级。
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
				Detail: "规则目录读取失败: " + err.Error(),
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

// DefaultProjectRulesDir 拼出 ./.ainovel/rules/ 的绝对路径（基于给定项目目录）。
// 调用方传入项目根，避免在 loader 内部依赖 cwd；镜像 DefaultHomeRulesDir。
func DefaultProjectRulesDir(projectDir string) string {
	if projectDir == "" {
		return ""
	}
	return filepath.Join(projectDir, ainovelDirName, "rules")
}

// DefaultHomeRulesDir 拼出 ~/.ainovel/rules/ 目录的绝对路径。
// home 解析失败返回空串（调用方据此跳过该来源）。
func DefaultHomeRulesDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ainovelDirName, "rules")
}

// homeRulesReadme 是首次引导时写入 ~/.ainovel/rules/README.txt 的说明。
// 刻意用 .txt 后缀而非 .md——loader 只扫描 .md，这份说明不会被当成规则注入 LLM。
const homeRulesReadme = `这里放全局写作偏好，跨所有书生效。

最简单：新建一个 .md 文件（如 my-style.md），用大白话写偏好就行——
不需要任何格式、不需要 YAML：

    # 角色
    - 主角林尘别写成圣母，外冷内热即可
    # 风格
    - 多用身体感知（指节发白）替代情绪标签（紧张）
    - 对话别太书面

这些会原样交给 editor 按语义审阅。多个 .md 按文件名字典序合并；
点开头的隐藏文件、非 .md 文件都会被忽略（所以这份 README.txt 不会被当成规则）。

进阶（可选）：想要"字数 / 禁词"这类硬性、确定的机械检查，
可在文件顶部加一段 YAML front matter——commit_chapter 会逐字计数、强制报错：

    ---
    chapter_words: 3000-6000          # 章节字数范围
    forbidden_phrases: ["某种程度上"]  # 禁用短语，出现即报错
    fatigue_words: {不禁: 1}           # 疲劳词，每章超阈值告警
    ---
    （下面照常写大白话偏好）

不写也没关系：常见 AI 套句、疲劳词的机械基线已内置，开箱即用。

加载优先级（高 → 低）：./.ainovel/rules/*.md（本书） > ~/.ainovel/rules/*.md（这里） > 内置默认
`

// EnsureHomeRulesDir 尽力创建 ~/.ainovel/rules/ 目录并写入 README.txt 引导，
// 让用户发现这个全局偏好扩展点、知道怎么写。
// nice-to-have，非关键路径：home 解析失败或写入出错都静默吞掉，绝不阻断启动。
func EnsureHomeRulesDir() {
	if dir := DefaultHomeRulesDir(); dir != "" {
		_ = ensureRulesDirAt(dir)
	}
}

// ensureRulesDirAt 创建目录并把 README.txt 写成当前引导模板，是 EnsureHomeRulesDir 的可测内核。
// README.txt 是系统生成的引导文件（用户偏好写在 *.md，它不被 loader 加载），每次都覆盖为
// 最新模板——不保留旧内容，也就不需要任何版本兼容逻辑。
func ensureRulesDirAt(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "README.txt"), []byte(homeRulesReadme), 0o644)
}

// DefaultOptions 根据当前工作目录构造常用 LoadOptions。
//
// 适合 Host 启动时调用一次，让 ContextTool / CommitChapterTool 复用同一份配置。
// 解析 cwd 失败时 ProjectRulesDir 留空（loader 会跳过该来源）。
//
// 路径语义：ProjectRulesDir 绑定 **当前工作目录（cwd）** 而非 outputDir。
// 用户 cd 到不同目录启动写不同的书，./.ainovel/rules/ 自然跟着 cwd 走；如需跨书共享，
// 放 ~/.ainovel/rules/ 全局目录即可（其下所有 .md 都会被加载）。
func DefaultOptions(rulesFS fs.FS) LoadOptions {
	cwd, _ := os.Getwd()
	return LoadOptions{
		RulesFS:         rulesFS,
		HomeRulesDir:    DefaultHomeRulesDir(),
		ProjectRulesDir: DefaultProjectRulesDir(cwd),
	}
}

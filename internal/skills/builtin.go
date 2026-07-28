package skills

import (
	"io/fs"
	"log/slog"
	"path"
	"sort"
	"strings"

	"github.com/voocel/ainovel-cli/assets"
)

// builtinSources 读取内置技能。
//
// 文件本体住在 assets/skills/（assets 是本项目唯一的资产家，见 assets/README.md），
// 由 assets.SkillsFS 暴露；解析与三层合并留在本包。go:embed 不能引用父目录，
// 所以只能是"assets 持有字节、skills 持有语义"这个分工。
//
// 内置资产读不出来属于构建问题而非用户问题，但同样只告警不 panic——skill 是增强
// 路径，缺了内置 skill 仍应能靠用户目录的 skill 与其余功能正常开书。
func builtinSources() []rawSource {
	fsys := assets.SkillsFS()
	entries, err := fs.ReadDir(fsys, "skills")
	if err != nil {
		slog.Warn("内置技能读取失败", "module", "skills", "err", err)
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(path.Ext(e.Name()), ".md") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	var out []rawSource
	for _, name := range names {
		data, err := fs.ReadFile(fsys, "skills/"+name)
		if err != nil {
			slog.Warn("内置技能读取失败", "module", "skills", "file", name, "err", err)
			continue
		}
		out = append(out, rawSource{label: "builtin:" + name, content: string(data)})
	}
	return out
}

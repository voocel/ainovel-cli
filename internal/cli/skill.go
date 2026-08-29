// Package cli 提供 ainovel 的辅助子命令（如 skill）。
// 这些命令在 main.go 顶部拦截，不参与 TUI/headless 主流程。
package cli

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/skills"
)

// SkillCommand 处理 `ainovel skill ...` 子命令。
func SkillCommand(args []string) int {
	if len(args) == 0 {
		printSkillUsage()
		return 0
	}

	store := skills.NewStore(filepath.Join(bootstrap.DefaultConfigDir(), "skills"))
	if err := store.Refresh(); err != nil {
		fmt.Fprintf(os.Stderr, "skill 库初始化失败: %v\n", err)
		return 1
	}

	switch args[0] {
	case "list", "ls":
		return runSkillList(store, args[1:])
	case "show", "cat":
		return runSkillShow(store, args[1:])
	case "add":
		return runSkillAdd(store, args[1:])
	case "edit":
		return runSkillEdit(store, args[1:])
	case "remove", "rm", "delete":
		return runSkillRemove(store, args[1:])
	case "refresh", "reload":
		return runSkillRefresh(store, args[1:])
	case "-h", "--help", "help":
		printSkillUsage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "未知子命令: %s\n", args[0])
		printSkillUsage()
		return 1
	}
}

func printSkillUsage() {
	fmt.Println("用法: ainovel skill <command> [args]")
	fmt.Println("")
	fmt.Println("命令:")
	fmt.Println("  list [category]      列出所有 skill（或某分类下）")
	fmt.Println("  show <name>          显示某 skill 全文")
	fmt.Println("  add <file>           从 .md 文件添加 skill（含 frontmatter 校验）")
	fmt.Println("  edit <name>          用 $EDITOR 编辑 skill")
	fmt.Println("  remove <name>        删除 skill（带确认）")
	fmt.Println("  refresh              强制刷新索引")
	fmt.Println("")
	fmt.Println("skill 存放位置: ~/.ainovel/skills/<category>/<name>.md")
}

func runSkillList(store *skills.Store, args []string) int {
	category := ""
	if len(args) > 0 {
		category = strings.TrimSpace(args[0])
	}
	metas := store.List(category)
	if len(metas) == 0 {
		fmt.Println("本地 skill 库为空。")
		return 0
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tCATEGORY\tPRIORITY\tDESCRIPTION")
	for _, m := range metas {
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", m.Name, m.Category, m.Priority, m.Description)
	}
	w.Flush()
	return 0
}

func runSkillShow(store *skills.Store, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "用法: ainovel skill show <name>")
		return 1
	}
	content, err := store.Read(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取失败: %v\n", err)
		return 1
	}
	fmt.Println(content)
	return 0
}

func runSkillAdd(store *skills.Store, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "用法: ainovel skill add <file.md>")
		return 1
	}
	path := args[0]
	raw, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取文件失败: %v\n", err)
		return 1
	}
	meta, body, err := skills.ParseSkill(path, string(raw))
	if err != nil {
		fmt.Fprintf(os.Stderr, "解析 skill 失败: %v\n", err)
		return 1
	}
	if meta.Name == "" {
		fmt.Fprintln(os.Stderr, "frontmatter 缺少 name 字段")
		return 1
	}
	if meta.Description == "" {
		fmt.Fprintln(os.Stderr, "frontmatter 缺少 description 字段")
		return 1
	}
	if err := store.Add(meta, body); err != nil {
		fmt.Fprintf(os.Stderr, "添加失败: %v\n", err)
		return 1
	}
	fmt.Printf("已添加 skill: %s (category=%s)\n", meta.Name, meta.Category)
	return 0
}

func runSkillEdit(store *skills.Store, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "用法: ainovel skill edit <name>")
		return 1
	}
	name := args[0]

	// 找到文件路径
	metas := store.List("")
	var target string
	for _, m := range metas {
		if m.Name == name {
			target = m.Path
			break
		}
	}
	if target == "" {
		fmt.Fprintf(os.Stderr, "skill %q 不存在\n", name)
		return 1
	}

	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = defaultEditor()
	}

	cmd := exec.Command(editor, target)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "编辑器退出失败: %v\n", err)
		return 1
	}
	if err := store.Refresh(); err != nil {
		fmt.Fprintf(os.Stderr, "刷新索引失败: %v\n", err)
		return 1
	}
	fmt.Printf("已更新 skill: %s\n", name)
	return 0
}

func runSkillRemove(store *skills.Store, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "用法: ainovel skill remove <name>")
		return 1
	}
	name := args[0]

	// 确认
	fmt.Printf("确认删除 skill %q 吗？输入 yes 继续: ", name)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取确认失败: %v\n", err)
		return 1
	}
	if strings.TrimSpace(strings.ToLower(line)) != "yes" {
		fmt.Println("已取消删除。")
		return 0
	}

	if err := store.Remove(name); err != nil {
		fmt.Fprintf(os.Stderr, "删除失败: %v\n", err)
		return 1
	}
	fmt.Printf("已删除 skill: %s\n", name)
	return 0
}

func runSkillRefresh(store *skills.Store, args []string) int {
	if err := store.Refresh(); err != nil {
		fmt.Fprintf(os.Stderr, "刷新失败: %v\n", err)
		return 1
	}
	fmt.Println("skill 索引已刷新。")
	return 0
}

func defaultEditor() string {
	if os.PathSeparator == '\\' {
		return "notepad"
	}
	return "vi"
}

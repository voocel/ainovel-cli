package notify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestAllowsFilter(t *testing.T) {
	if New("", nil).allows(KindDeadlock) != true {
		t.Error("events 缺省应全放行")
	}
	n := New("", []string{KindRunEnd, KindBudget})
	if !n.allows(KindRunEnd) || !n.allows(KindBudget) {
		t.Error("列入的 kind 应放行")
	}
	if n.allows(KindDeadlock) {
		t.Error("未列入的 kind 应拦截")
	}
	var nilN *Notifier
	if nilN.allows(KindRunEnd) {
		t.Error("nil Notifier 应拦截一切")
	}
	nilN.Send(Notification{Kind: KindRunEnd}) // 不应 panic
}

func TestKindsAreUniqueAndKnown(t *testing.T) {
	seen := map[string]bool{}
	for _, kind := range Kinds() {
		if kind == "" || seen[kind] {
			t.Fatalf("通知事件名必须非空且唯一: %q", kind)
		}
		seen[kind] = true
		if !IsKnownKind(kind) {
			t.Fatalf("Kinds 与 IsKnownKind 不一致: %q", kind)
		}
	}
	if IsKnownKind("repeat") {
		t.Fatal("旧 repeat 事件不应继续出现在新契约中")
	}
}

func TestCommandChannelEnvAndStdin(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "env.txt")
	jsonFile := filepath.Join(dir, "stdin.json")

	command := `echo "$NOTIFY_KIND|$NOTIFY_LEVEL|$NOTIFY_TITLE|$NOTIFY_BODY" > ` + shellQuote(envFile) + ` && cat > ` + shellQuote(jsonFile)
	if runtime.GOOS == "windows" {
		// Explicit UTF-8 (no BOM) so Chinese title/body survive PowerShell's default code page.
		command = `$utf8 = New-Object System.Text.UTF8Encoding $false; ` +
			`$line = "$env:NOTIFY_KIND|$env:NOTIFY_LEVEL|$env:NOTIFY_TITLE|$env:NOTIFY_BODY"; ` +
			`[System.IO.File]::WriteAllText(` + powerShellQuote(envFile) + `, $line, $utf8); ` +
			`$reader = New-Object System.IO.StreamReader([Console]::OpenStandardInput(), $utf8); ` +
			`$payload = $reader.ReadToEnd(); ` +
			`[System.IO.File]::WriteAllText(` + powerShellQuote(jsonFile) + `, $payload, $utf8)`
	}
	n := New(command, nil)
	nt := Notification{Kind: KindBudget, Level: "warn", Title: "ainovel: 预算", Body: "已花费 $8.00"}
	n.deliver(nt) // 同步调用以便断言

	env, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("command 未执行: %v", err)
	}
	if got := strings.TrimSpace(string(env)); got != "budget|warn|ainovel: 预算|已花费 $8.00" {
		t.Errorf("环境变量传递不符: %q", got)
	}

	raw, err := os.ReadFile(jsonFile)
	if err != nil {
		t.Fatalf("stdin 未传递: %v", err)
	}
	var decoded Notification
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("stdin 非合法 JSON: %v", err)
	}
	if decoded != nt {
		t.Errorf("stdin JSON 不符: %+v", decoded)
	}
}

func TestCommandChannelTimeoutKill(t *testing.T) {
	command := "sleep 30"
	if runtime.GOOS == "windows" {
		command = "Start-Sleep -Seconds 30"
	}
	n := New(command, nil)
	n.timeout = 200 * time.Millisecond

	start := time.Now()
	n.deliver(Notification{Kind: KindRunEnd})
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("超时未强杀, 阻塞 %v", elapsed)
	}
}

func TestWindowsNotificationScriptUsesEnvironmentWithoutInterpolation(t *testing.T) {
	for _, want := range []string{"$env:NOTIFY_TITLE", "$env:NOTIFY_BODY", "$env:NOTIFY_LEVEL", "ShowBalloonTip"} {
		if !strings.Contains(windowsNotificationScript, want) {
			t.Fatalf("Windows notification script missing %q", want)
		}
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func powerShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

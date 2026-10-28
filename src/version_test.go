package main

import (
	"regexp"
	"strings"
	"testing"
)

// 版本号格式锁死成 主.次.修订，三段的十进制数。
// 之所以要测：这个值会被 build.sh 用 cut -d. -f1/2/3 拆成三段喂给 Windows
// 版本资源，一旦写成 "1.0" 或 "v1.0.0"，拆分结果就是空的或带字母，
// goversioninfo 生成的 exe 属性会变成 0.0.0.0，而且构建不会报错。
func TestAppVersionFormat(t *testing.T) {
	re := regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	if !re.MatchString(appVersion) {
		t.Fatalf("appVersion = %q，必须形如 1.0.0（不带 v 前缀、必须有三位）", appVersion)
	}
	if strings.HasPrefix(appVersion, "v") {
		t.Fatalf("appVersion 不能带 v 前缀，展示时由 versionText 补：%q", appVersion)
	}
}

func TestVersionText(t *testing.T) {
	old := appBuild
	defer func() { appBuild = old }()

	appBuild = ""
	if got := versionText(); got != "v"+appVersion {
		t.Fatalf("无构建标识时 versionText = %q, want %q", got, "v"+appVersion)
	}

	appBuild = "20261028-2136"
	want := "v" + appVersion + " build 20261028-2136"
	if got := versionText(); got != want {
		t.Fatalf("versionText = %q, want %q", got, want)
	}
	// 启动日志/托盘 Tooltip 用的详情串必须同时带上版本和平台架构
	d := versionDetail()
	for _, sub := range []string{appVersion, "build", "20261028-2136"} {
		if !strings.Contains(d, sub) {
			t.Fatalf("versionDetail = %q，缺少 %q", d, sub)
		}
	}
}

package main

import (
	"regexp"
	"strings"
	"testing"
)

// 版本号格式锁成 主.次 或 主.次.修订，都是十进制。
//
// 之所以要测：这个值会被 build.sh 用 cut -d. 拆成三段喂给 Windows 版本资源，
// 一旦写成 "v1.0" 或 "1" 这种形式，拆分结果就是空的或带字母，goversioninfo
// 生成的 exe 属性会变成 0.0.0.0——而且构建过程一声不响，只能靠这条测试拦住。
func TestAppVersionFormat(t *testing.T) {
	re := regexp.MustCompile(`^\d+\.\d+(\.\d+)?$`)
	if !re.MatchString(appVersion) {
		t.Fatalf("appVersion = %q，必须形如 1.0 或 1.0.1（不带 v 前缀、至少两段）", appVersion)
	}
	if strings.HasPrefix(appVersion, "v") {
		t.Fatalf("appVersion 不能带 v 前缀，展示时由 versionText 补：%q", appVersion)
	}
	// 构建脚本按 "." 拆分后要直接转成整数喂给 goversioninfo，
	// 每段必须是纯数字且不超过 65535（Windows 版本资源每段是 16 位）
	for _, seg := range strings.Split(appVersion, ".") {
		if len(seg) > 5 {
			t.Fatalf("appVersion 的段 %q 过长，Windows 版本资源每段上限 65535", seg)
		}
	}
}

// 菜单里显示 "版本 v1.0 build 20261028"：版本号 + 日期，不带时分。
func TestVersionLabel(t *testing.T) {
	old := appBuild
	defer func() { appBuild = old }()

	appBuild = ""
	if got := versionLabel(); got != "v"+appVersion {
		t.Fatalf("无构建号时 versionLabel = %q, want %q", got, "v"+appVersion)
	}

	appBuild = "20261028"
	want := "v" + appVersion + " build 20261028"
	if got := versionLabel(); got != want {
		t.Fatalf("versionLabel = %q, want %q", got, want)
	}
	// 菜单项整体要短，否则托盘里会被系统截断
	menu := "版本 " + versionLabel()
	if n := len([]rune(menu)); n > 26 {
		t.Fatalf("菜单项「%s」共 %d 字，太长", menu, n)
	}
	// 不能出现时分：出现过 "-2142" 这种尾巴，用户嫌太长
	if strings.Contains(menu, "-") {
		t.Fatalf("菜单项「%s」不该带时分连字符", menu)
	}
	// versionText 保持纯版本号，别把构建号混进去
	if strings.Contains(versionText(), "build") {
		t.Fatalf("versionText = %q 不该含 build", versionText())
	}
}

// 排查用的详情串必须带构建日期和平台架构。
func TestVersionDetail(t *testing.T) {
	old := appBuild
	defer func() { appBuild = old }()

	appBuild = "20261028"
	d := versionDetail()
	for _, sub := range []string{appVersion, "build", "20261028"} {
		if !strings.Contains(d, sub) {
			t.Fatalf("versionDetail = %q，缺少 %q", d, sub)
		}
	}

	appBuild = ""
	if d := versionDetail(); !strings.Contains(d, "本地构建") {
		t.Fatalf("无构建号时 versionDetail = %q，应标注本地构建", d)
	}
}

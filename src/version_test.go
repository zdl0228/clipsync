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

// 界面上的版本串只带版本号本身，不能混进构建时间戳。
// 这是用户明确要求过的：托盘菜单里 "版本 v1.0.0 build 20261028-2136" 太长。
func TestVersionTextIsShort(t *testing.T) {
	old := appBuild
	defer func() { appBuild = old }()

	appBuild = "20261028-2136"
	if got := versionText(); got != "v"+appVersion {
		t.Fatalf("versionText = %q, want %q（不能带 build 号）", got, "v"+appVersion)
	}
	if strings.Contains(versionText(), appBuild) {
		t.Fatalf("versionText = %q 混进了构建标识 %q", versionText(), appBuild)
	}
	// 菜单项整体也要短，太长的菜单在托盘里会被截断
	if n := len([]rune("版本 " + versionText())); n > 12 {
		t.Fatalf("菜单项「版本 %s」共 %d 字，太长", versionText(), n)
	}
}

// 排查用的详情串反过来必须带构建标识，否则同一版本号的多次构建分不清。
func TestVersionDetailHasBuild(t *testing.T) {
	old := appBuild
	defer func() { appBuild = old }()

	appBuild = "20261028-2136"
	d := versionDetail()
	for _, sub := range []string{appVersion, "build", "20261028-2136"} {
		if !strings.Contains(d, sub) {
			t.Fatalf("versionDetail = %q，缺少 %q", d, sub)
		}
	}

	appBuild = ""
	if d := versionDetail(); !strings.Contains(d, "本地构建") {
		t.Fatalf("无构建标识时 versionDetail = %q，应标注本地构建", d)
	}
}

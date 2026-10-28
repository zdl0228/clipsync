package main

import (
	"fmt"
	"runtime"
)

// appVersion 是唯一的版本号来源。
//
// 发布一个新版本时只改这里，然后用 tools/build.sh 打包——脚本会读取这个值
// 去同步 Info.plist、Windows 版本资源和 dmg 卷名，避免三处各写一份对不上。
// 格式固定为 主版本.次版本.修订号（语义化版本），别加 "v" 前缀，v 在展示时补。
//
// 为什么版本号这么重要：Windows 上装了新版但托盘行为没变，第一件要确认的
// 就是"现在跑的到底是哪一版"，没有版本号就只能靠文件时间戳猜。
const appVersion = "1.0.0"

// appBuild 是构建标识（时间戳或 git 短哈希），由 -ldflags
// -X main.appBuild=xxx 注入；源码里保持空，方便直接 go run 调试。
var appBuild = ""

// versionText 返回展示用的版本串，例如 "v1.0.0" 或 "v1.0.0 build 20261028-2130"。
func versionText() string {
	if appBuild == "" {
		return "v" + appVersion
	}
	return "v" + appVersion + " build " + appBuild
}

// versionDetail 是启动日志/关于弹窗里用的多行信息：版本、架构、构建标识。
// GOOS/GOARCH 一定要带上——Windows 的 exe 是从 macOS 交叉编译出来的，
// 排查问题时"这是哪个平台的包"经常是第一个要问的。
func versionDetail() string {
	s := fmt.Sprintf("ClipSync %s (%s/%s)", versionText(), runtime.GOOS, runtime.GOARCH)
	if appBuild == "" {
		s += " 本地构建"
	}
	return s
}

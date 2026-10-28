package main

import (
	"fmt"
	"runtime"
)

// appVersion 是唯一的版本号来源。
//
// 发布新版本时只改这里，然后跑 build/build.sh 打包——脚本会读取这个值去同步
// Info.plist、Windows 版本资源和 dmg 卷名，避免几处各写一份对不上。
//
// 格式：主版本.次版本，允许再加一段修订号（如 1.0.2）。不要带 "v" 前缀，v 在展示时补。
// 界面上只显示这个值（"版本 v1.0"），构建时间戳不进菜单——那一串 20261028-2136
// 挂在托盘里太长太吵，排查时看日志或 --version 就够。
const appVersion = "1.0"

// appBuild 是构建标识（时间戳），由 -ldflags -X main.appBuild=xxx 注入；
// 源码里保持空，方便直接 go run 调试。
var appBuild = ""

// versionText 是界面上显示的版本串，例如 "v1.0"。
// 托盘菜单、tooltip 都用它——故意不含构建号，避免菜单项过长。
func versionText() string {
	return "v" + appVersion
}

// versionDetail 是排查用的完整信息：版本、平台架构、构建标识。
// 只出现在 --version 输出和启动日志里，不进界面。
//
// GOOS/GOARCH 一定要带：Windows 的 exe 是从 macOS 交叉编译出来的，
// 出问题时"这是哪个平台的包"经常是第一个要问的。
func versionDetail() string {
	s := fmt.Sprintf("ClipSync %s (%s/%s)", versionText(), runtime.GOOS, runtime.GOARCH)
	if appBuild == "" {
		return s + " 本地构建"
	}
	return s + " build " + appBuild
}

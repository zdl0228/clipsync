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
const appVersion = "1.0"

// appBuild 是构建日期（YYYYMMDD），由 -ldflags -X main.appBuild=xxx 注入；
// 源码里保持空，方便直接 go run 调试。
//
// 只到天、不带时分：它要显示在托盘菜单里，带上时分那串 "20261028-2142" 太长。
// 同一天编多次会得到同一个构建号，这是可以接受的——真正需要区分到分钟的场景
// （比对两个包是不是同一次编的）看文件时间戳就够了。
var appBuild = ""

// versionText 只返回版本号本身，例如 "v1.0"。
// 用在需要"纯版本"的地方，不掺构建日期。
func versionText() string {
	return "v" + appVersion
}

// versionLabel 是界面上显示的完整标签，例如 "v1.0 build 20261028"。
// 托盘菜单、tooltip 用它——带上构建日期，两台机器装的包不一样时一眼能看出来。
// appBuild 为空（本地 go run）时自动退化成 "v1.0"。
func versionLabel() string {
	if appBuild == "" {
		return versionText()
	}
	return versionText() + " build " + appBuild
}

// versionDetail 是排查用的完整信息：版本、构建日期、平台架构。
// 只出现在 --version 输出和启动日志里。
//
// GOOS/GOARCH 一定要带：Windows 的 exe 是从 macOS 交叉编译出来的，
// 出问题时"这是哪个平台的包"经常是第一个要问的。
func versionDetail() string {
	s := fmt.Sprintf("ClipSync %s (%s/%s)", versionLabel(), runtime.GOOS, runtime.GOARCH)
	if appBuild == "" {
		s += " 本地构建"
	}
	return s
}

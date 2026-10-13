package main

import (
	"sync"

	"github.com/getlantern/systray"
)

// 传输进度与进行中任务计数：并发传输时加锁保护，避免互相覆盖/提前隐藏
var (
	progressMu      sync.Mutex
	activeTransfers int
	progressItem    *systray.MenuItem
)

// progressBegin 登记一个进行中的传输并显示进度行
func progressBegin() {
	progressMu.Lock()
	activeTransfers++
	if progressItem != nil {
		progressItem.Show()
		progressItem.Enable()
	}
	progressMu.Unlock()
}

// progressSet 更新进度行标题（仅在有进行中任务时生效）
func progressSet(title string) {
	progressMu.Lock()
	if progressItem != nil && activeTransfers > 0 {
		progressItem.SetTitle(title)
	}
	progressMu.Unlock()
}

// progressEnd 结束一个传输；全部结束后隐藏进度行
func progressEnd() {
	progressMu.Lock()
	if activeTransfers > 0 {
		activeTransfers--
	}
	if activeTransfers <= 0 && progressItem != nil {
		progressItem.SetTitle("就绪")
		progressItem.Disable()
		progressItem.Hide()
	}
	progressMu.Unlock()
}

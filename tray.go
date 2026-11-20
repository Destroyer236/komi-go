package main

import (
	_ "embed"
	"fmt"

	"github.com/getlantern/systray"
)

//go:embed build/windows/icon.ico
var trayIcon []byte

func (a *App) startTray() {
	go systray.Run(a.onTrayReady, func() {
		a.setTrayReady(false)
	})
}

func (a *App) isTrayReady() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.trayReady
}

func (a *App) setTrayReady(v bool) {
	a.mu.Lock()
	a.trayReady = v
	a.mu.Unlock()
}

func (a *App) onTrayReady() {
	systray.SetIcon(trayIcon)
	systray.SetTitle("Komi Go")
	systray.SetTooltip("Komi Go · GitHub 桌面应用商店")

	mOpen := systray.AddMenuItem("打开 Komi Go", "显示主窗口")
	mUpdates := systray.AddMenuItem("检查更新", "检查已安装应用的新版本")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("退出", "退出 Komi Go")

	a.setTrayReady(true)
	a.emit("tray:ready", true)

	for {
		select {
		case <-mOpen.ClickedCh:
			a.showWindow()
		case <-mUpdates.ClickedCh:
			go func() {
				apps, err := a.CheckUpdates(true)
				if err != nil {
					systray.SetTooltip("检查失败：" + friendly(err))
					return
				}
				count := 0
				for _, app := range apps {
					if app.UpdateAvailable {
						count++
					}
				}
				systray.SetTooltip(fmt.Sprintf("Komi Go · %d 个可用更新", count))
				a.emit("updates:checked", map[string]any{"count": count})
			}()
		case <-mQuit.ClickedCh:
			go a.Quit()
			return
		}
	}
}

func (a *App) stopTray() {
	if a.isTrayReady() {
		systray.Quit()
		a.setTrayReady(false)
	}
}

func setTrayTooltip(text string) {
	systray.SetTooltip(text)
}

package main

import (
	"embed"
	"io"
	"log"
	"os"
	"path/filepath"

	"komi-go/internal/platform"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	initLogging()
	app, err := NewApp()
	if err != nil {
		log.Fatal(err)
	}

	err = wails.Run(&options.App{
		Title:     "Komi Go",
		Width:     1240,
		Height:    820,
		MinWidth:  960,
		MinHeight: 640,
		Frameless: true,
		// M3 light surface; the JS theme switch repaints immediately.
		BackgroundColour: &options.RGBA{R: 247, G: 251, B: 243, A: 255},
		AssetServer:      &assetserver.Options{Assets: assets},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		OnBeforeClose:    app.beforeClose,
		Bind:             []interface{}{app},
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: "komi-go-main",
			OnSecondInstanceLaunch: func(data options.SecondInstanceData) {
				app.onSecondInstance(data.Args)
			},
		},
		Windows: &windows.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			DisableWindowIcon:    false,
			// Pin zoom to 1.0. WebView2 persists the last zoom in its profile,
			// so leaving it unset can inherit a stale value from a test build.
			ZoomFactor: 1.0,
			// Keep WebView2's profile under the app data directory. Besides
			// keeping the machine tidy, a dedicated profile makes cache
			// invalidation predictable across builds.
			WebviewUserDataPath: filepath.Join(os.Getenv("LOCALAPPDATA"), "KomiGo", "WebView2"),
		},
	})
	if err != nil {
		log.Fatal(err)
	}
}

// initLogging mirrors log output into %LocalAppData%\KomiGo\logs\komi.log.
// A GUI build has no console, so a failed startup would otherwise leave no
// trace at all. The file is rotated once at 8 MB to keep it bounded.
func initLogging() {
	layout, err := platform.EnsureLayout()
	if err != nil {
		return
	}
	path := filepath.Join(layout.Logs, "komi.log")
	if fi, statErr := os.Stat(path); statErr == nil && fi.Size() > 8<<20 {
		_ = os.Rename(path, path+".1")
	}
	f, openErr := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if openErr != nil {
		return
	}
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.SetOutput(io.MultiWriter(os.Stderr, f))
}

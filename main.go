package main

import (
	"embed"
	"fmt"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:generate go run ./cmd/icongen

//go:embed all:frontend/dist
var assets embed.FS

// version is the release version. The release workflow sets it with
// -ldflags "-X main.version=<tag>"; local builds report "dev".
var version = "dev"

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "--version" || os.Args[1] == "-version") {
		fmt.Println("tokenmetrics", version)
		return
	}

	// Create an instance of the app structure
	app := NewApp()
	life := newLifecycle(app)

	// Create application with options
	err := wails.Run(&options.App{
		Title:     "TokenMetrics",
		Width:     1180,
		Height:    820,
		MinWidth:  360,
		MinHeight: 480,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 13, G: 13, B: 13, A: 255},
		OnStartup:        life.startup,
		OnShutdown:       life.shutdown,
		// With a tray icon the window is hidden, not closed: the application
		// goes on refreshing and notifying. Quit is in the tray menu.
		HideWindowOnClose: life.tray,
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId:               singleInstanceID,
			OnSecondInstanceLaunch: life.onSecondLaunch,
		},
		Windows: &windows.Options{
			Theme: windows.SystemDefault,
		},
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}

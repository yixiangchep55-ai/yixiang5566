package main

import (
	"embed"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

// Explicit patterns are more reliable here than embedding the directory root
// with `all:frontend/dist`, which has been flaky in this Windows toolchain.
//
//go:embed frontend/dist/index.html frontend/dist/assets/*
var assets embed.FS

func main() {
	app := NewApp()

	err := wails.Run(&options.App{
		Title:     "MyCoin Dashboard",
		Width:     560,
		Height:    460,
		MinWidth:  540,
		MinHeight: 430,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 10, G: 16, B: 32, A: 1},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}

package main

import (
	"embed"
	"fmt"
	"os"

	"gcp-mailnode/internal/logger"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

var Version = "0.1.85"

func main() {
	defer func() {
		if r := recover(); r != nil {
			logger.LogPanic(r)
			fmt.Fprintf(os.Stderr, "PANIC: %v\n", r)
			os.Exit(1)
		}
	}()

	app := NewApp()

	err := wails.Run(&options.App{
		Title:     fmt.Sprintf("GCP MailNode  v%s", Version),
		Width:     1600,
		Height:    1000,
		MinWidth:  1200,
		MinHeight: 720,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 27, G: 38, B: 54, A: 1},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err.Error())
		os.Exit(1)
	}
}

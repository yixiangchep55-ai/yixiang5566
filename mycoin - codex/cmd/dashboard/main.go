//go:build desktopapp
// +build desktopapp

package main

import (
	"flag"

	"mycoin/dashboard"
)

func main() {
	apiURL := flag.String("api", "http://localhost:8080", "Local mycoin API base URL")
	nodeExe := flag.String("node-exe", "", "Path to local node executable (defaults to a sibling mycoin-node.exe)")
	autoStart := flag.Bool("autostart-node", true, "Automatically launch the local node when the dashboard API is offline")
	flag.Parse()

	dashboard.NewWithOptions(dashboard.Options{
		BaseURL:        *apiURL,
		NodeExecutable: *nodeExe,
		AutoStartNode:  *autoStart,
	}).Run()
}

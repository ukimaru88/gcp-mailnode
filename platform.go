package main

import "os/exec"

func openExplorer(path string) error {
	return exec.Command("explorer", path).Start()
}

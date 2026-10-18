package main

import (
	"os"
	"time"

	"github.com/lavr/regru-cli/internal/cli"
)

func isCharDevice(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func main() {
	home, _ := os.UserHomeDir()
	env := cli.Env{
		Stdout:    os.Stdout,
		Stderr:    os.Stderr,
		Stdin:     os.Stdin,
		LookupEnv: os.LookupEnv,
		IsTTY:     func() bool { return isCharDevice(os.Stdin) && isCharDevice(os.Stderr) },
		Now:       time.Now,
		Home:      home,
	}
	os.Exit(cli.Execute(os.Args[1:], env, nil))
}

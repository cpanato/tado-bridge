// observability prepares and exercises the optional local Compose stack.
// It is a development tool and is never included in production images.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

const purpose = "tado-bridge-synthetic-preview"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	root, err := repositoryRoot()
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return errors.New("usage: observability prepare DIRECTORY | smoke [--keep] [--screenshot FILE] | stop STATE_FILE")
	}
	switch args[0] {
	case "prepare":
		if len(args) != 2 {
			return errors.New("prepare requires a private state directory")
		}
		if err := prepare(root, args[1], os.Getenv("API_TOKEN")); err != nil {
			return errors.New("preparation failed: check API_TOKEN and owned, mode-0700 state directories")
		}
		fmt.Println("Private observability state and dashboard prepared.")
		return nil
	case "smoke":
		flags := flag.NewFlagSet("smoke", flag.ContinueOnError)
		keep := flags.Bool("keep", false, "retain the validated synthetic preview")
		screenshot := flags.String("screenshot", "", "capture the synthetic dashboard (Node and Chrome required)")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("unexpected smoke arguments")
		}
		return smoke(root, *keep, *screenshot)
	case "stop":
		if len(args) != 2 {
			return errors.New("stop requires a retained preview-state.json file")
		}
		return stop(root, args[1], execute)
	default:
		return errors.New("unknown observability command")
	}
}

func repositoryRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "compose.observability.yaml")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("run this tool from the tado-bridge repository")
		}
		dir = parent
	}
}

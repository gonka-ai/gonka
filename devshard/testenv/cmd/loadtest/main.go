// Binary loadtest runs one declarative Devshard load scenario in an isolated Docker stack.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"devshard/testenv/loadtest"
)

func main() {
	scenarioPath := flag.String("scenario", "", "path to a load scenario YAML")
	profilesDir := flag.String("profiles-dir", "", "directory containing ML profile YAML files")
	outputDir := flag.String("output", "", "directory for run artifacts")
	keepStack := flag.Bool("keep-stack", false, "keep the Docker stack and work directory after the run")
	flag.Parse()
	if *scenarioPath == "" {
		log.Fatal("-scenario is required")
	}
	testenvDir, err := filepath.Abs(".")
	if err != nil {
		log.Fatal(err)
	}
	if *outputDir == "" {
		*outputDir = filepath.Join(testenvDir, "loadtest", "results", time.Now().UTC().Format("20060102T150405Z"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	result, err := loadtest.RunScenario(ctx, loadtest.RunnerConfig{
		ScenarioPath: *scenarioPath,
		ProfilesDir:  *profilesDir,
		TestenvDir:   testenvDir,
		OutputDir:    *outputDir,
		KeepStack:    *keepStack,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Fprintf(os.Stdout, "scenario=%s requests=%d completed=%d failed=%d output=%s\n", result.Summary.Scenario, result.Summary.Requests, result.Summary.Completed, result.Summary.Failed, result.OutputDir)
}

package main

import (
        "encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bitrise-io/go-steputils/stepconf"
	"github.com/bitrise-io/go-steputils/tools"
	"github.com/bitrise-io/go-utils/log"
	"github.com/bitrise-steplib/bitrise-step-build-router-start/bitrise"
)

const envBuildSlugs = "ROUTER_STARTED_BUILD_SLUGS"

// Config ...
type Config struct {
	AppSlug                string          `env:"BITRISE_APP_SLUG,required"`
	BuildSlug              string          `env:"BITRISE_BUILD_SLUG,required"`
	BuildNumber            string          `env:"BITRISE_BUILD_NUMBER,required"`
	AccessToken            stepconf.Secret `env:"access_token,required"`
	WaitForBuilds          string          `env:"wait_for_builds"`
	BuildArtifactsSavePath string          `env:"build_artifacts_save_path"`
	AbortBuildsOnFail      string          `env:"abort_on_fail"`
	Workflows              string          `env:"workflows,required"`
	Environments           string          `env:"environment_key_list"`
	IsVerboseLog           bool            `env:"verbose,required"`
}

func failf(s string, a ...interface{}) {
	log.Errorf(s, a...)
	os.Exit(1)
}

func main() {
	var cfg Config
	if err := stepconf.Parse(&cfg); err != nil {
		failf("Issue with an input: %s", err)
	}

	stepconf.Print(cfg)
	fmt.Println()

	log.SetEnableDebugLog(cfg.IsVerboseLog)

	app := bitrise.NewAppWithDefaultURL(cfg.AppSlug, string(cfg.AccessToken))

	build, err := app.GetBuild(cfg.BuildSlug)
	if err != nil {
		failf("failed to get build, error: %s", err)
	}

	log.Infof("Starting builds:")

	var buildSlugs []string
	environments := createEnvs(cfg.Environments)
	for _, wf := range strings.Split(strings.TrimSpace(cfg.Workflows), "\n") {
		wf = strings.TrimSpace(wf)
		params := strings.Split(wf, "|")
		if len(params) == 2 {
			wf = params[0]
			jsonStr := []byte(params[1])
			var jsonObj map[string]string
			if err := json.Unmarshal(jsonStr, &jsonObj); err != nil {
				log.Printf("Failed to decode Env Var JSON:\n\tJSON: %s\n\tError: %s", jsonStr, err)
				log.Printf("Example Usage:\n\tworkflow_name|{\"MY_ENV_VAR\": \"1\"}")
			}
			for key := range jsonObj {
				value := jsonObj[key]
				log.Printf("Passing Env Var: %s Value: %s", key, value)
				env := bitrise.Environment{
					MappedTo: key,
					Value:    value,
				}
				environments = append(environments, env)
			}
		}
                startedBuild, err := app.StartBuild(wf, build.OriginalBuildParams, cfg.BuildNumber, environments)
		if err != nil {
			failf("Failed to start build, error: %s", err)
		}
		if startedBuild.BuildSlug == "" {
			failf("Build was not started. This could mean that manual build approval is enabled for this project and it's blocking this step from starting builds.")
		}
		buildSlugs = append(buildSlugs, startedBuild.BuildSlug)
		log.Printf("- %s started (https://app.bitrise.io/build/%s)", startedBuild.TriggeredWorkflow, startedBuild.BuildSlug)
	}

	if err := tools.ExportEnvironmentWithEnvman(envBuildSlugs, strings.Join(buildSlugs, "\n")); err != nil {
		failf("Failed to export environment variable, error: %s", err)
	}

	if cfg.WaitForBuilds != "true" {
		return
	}

	fmt.Println()
	log.Infof("Waiting for builds:")

	if err := app.WaitForBuilds(buildSlugs, func(build bitrise.Build) {
		var failReason string
		switch build.Status {
		case 0:
			log.Printf("- %s %s", build.TriggeredWorkflow, build.StatusText)
		case 1:
			log.Donef("- %s successful", build.TriggeredWorkflow)
		case 2:
			log.Errorf("- %s failed", build.TriggeredWorkflow)
			failReason = "failed"
		case 3:
			log.Warnf("- %s aborted", build.TriggeredWorkflow)
			failReason = "aborted"
		case 4:
			log.Infof("- %s cancelled", build.TriggeredWorkflow)
			failReason = "cancelled"
		}

		if cfg.AbortBuildsOnFail == "yes" && build.Status > 1 {
			for _, buildSlug := range buildSlugs {
				if buildSlug != build.Slug {
					abortErr := app.AbortBuild(buildSlug, "Abort on Fail - Build [https://app.bitrise.io/build/"+build.Slug+"] "+failReason+"\nAuto aborted by parent build")
					if abortErr != nil {
						log.Warnf("failed to abort build, error: %s", abortErr)
					}
					log.Donef("Build " + buildSlug + " aborted due to associated build failure")
				}
			}
		}

		if build.Status != 0 {
			buildArtifactSaveDir := strings.TrimSpace(cfg.BuildArtifactsSavePath)
			if buildArtifactSaveDir != "" {
				artifactsResponse, err := build.GetBuildArtifacts(app)
				if err != nil {
					log.Warnf("failed to get build artifacts: %s", err)
				}
				for _, artifactSlug := range artifactsResponse.ArtifactSlugs {
					artifactObj, err := build.GetBuildArtifact(app, artifactSlug.ArtifactSlug)
					if err != nil {
						log.Warnf("failed to get build artifact: %s", err)
						continue
					}
					if err = os.MkdirAll(buildArtifactSaveDir, 0777); err != nil {
						log.Warnf("failed to ensure artifact path %s exists: %s", buildArtifactSaveDir, err)
						continue
					}
					fullBuildArtifactsSavePath := filepath.Join(buildArtifactSaveDir, artifactObj.Artifact.Title)
					downloadErr := artifactObj.Artifact.DownloadArtifact(fullBuildArtifactsSavePath)
					if downloadErr != nil {
						log.Warnf("failed to download %s artifact: %s", artifactObj.Artifact.Title, downloadErr)
					} else {
						log.Donef("Downloaded %s to %s", artifactObj.Artifact.Title, fullBuildArtifactsSavePath)
					}
				}
			}
		}
	}); err != nil {
		failf("An error occurred: %s", err)
	}
}

func createEnvs(environmentKeys string) []bitrise.Environment {
	environmentKeys = strings.Replace(environmentKeys, "$", "", -1)
	environmentsKeyList := strings.Split(environmentKeys, "\n")

	var environments []bitrise.Environment
	for _, key := range environmentsKeyList {
		if key == "" {
			continue
		}

		env := bitrise.Environment{
			MappedTo: key,
			Value:    os.Getenv(key),
		}
		environments = append(environments, env)
	}
	return environments
}

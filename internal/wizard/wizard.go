package wizard

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/ericdahl-dev/aws-green/internal/config"
)

// ErrUserAborted is returned when the user cancels the init form.
var ErrUserAborted = huh.ErrUserAborted

// RunInteractive collects one project via Huh and writes a starter config.
func RunInteractive(path string, force bool) error {
	if _, err := os.Stat(path); err == nil && !force {
		return fmt.Errorf("config already exists at %s (use --force to overwrite)", path)
	}

	var accountName, profile, region, projectName, pipeline string
	region = "us-east-1"

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Account name").
				Description("A label for this AWS account (e.g. prod, staging).").
				Value(&accountName).
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return errors.New("account name is required")
					}
					return nil
				}),
			huh.NewInput().
				Title("AWS profile").
				Description("AWS CLI profile to use for this account (from ~/.aws/config).").
				Value(&profile).
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return errors.New("AWS profile is required")
					}
					return nil
				}),
			huh.NewInput().
				Title("AWS region").
				Description("Region where your CodePipeline lives.").
				Value(&region).
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return errors.New("region is required")
					}
					return nil
				}),
		).Title("aws-green init · Account").Description("Configure your AWS account"),
		huh.NewGroup(
			huh.NewInput().
				Title("Project name").
				Description("A display name for this project (e.g. my-app).").
				Value(&projectName).
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return errors.New("project name is required")
					}
					return nil
				}),
			huh.NewInput().
				Title("CodePipeline name").
				Description("The exact name of the pipeline in AWS CodePipeline.").
				Value(&pipeline).
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return errors.New("pipeline name is required")
					}
					return nil
				}),
		).Title("aws-green init · Project").Description("Configure your first project"),
	)

	if err := form.Run(); err != nil {
		return err
	}

	return config.WriteStarter(path, accountName, profile, region, projectName, pipeline)
}

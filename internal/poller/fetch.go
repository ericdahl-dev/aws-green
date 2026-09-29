package poller

import (
	"context"
	"fmt"
	"time"

	"github.com/ericdahl-dev/aws-green/internal/cfn"
	"github.com/ericdahl-dev/aws-green/internal/config"
	"github.com/ericdahl-dev/aws-green/internal/ecs"
	"github.com/ericdahl-dev/aws-green/internal/health"
	"github.com/ericdahl-dev/aws-green/internal/state"
)

// accountClients are the fetchers for one Account. A nil fetcher with a nil
// error means that kind of resource isn't supported at all (its factory was
// never wired up); a non-nil error means the client couldn't be built.
type accountClients struct {
	pipeline    Fetcher
	pipelineErr error
	cfn         cfn.Fetcher
	cfnErr      error
	ecs         ecs.Fetcher
	ecsErr      error
}

// fetchProject runs one poll cycle's fetches for a single project. Each kind
// of resource either comes back fresh or keeps its last known state from prev,
// stamped stale at now — so a broken call is distinguishable from a project
// that has nothing of that kind configured, and the dashboard doesn't blank
// on a transient failure. The Profile and Region are left to the caller.
func fetchProject(ctx context.Context, proj config.Project, c accountClients, prev state.ProjectState, now time.Time) state.ProjectState {
	out := state.ProjectState{Name: proj.Name, Account: proj.Account}
	stale := func(err error) state.FetchStatus {
		return state.FetchStatus{StaleAt: &now, Err: err}
	}

	switch {
	case c.pipeline == nil:
		out.Pipeline = prev.Pipeline
		out.Pipeline.FetchStatus = stale(noClient("", proj.Account, c.pipelineErr))
	case proj.Pipeline.Name == "":
		out.Pipeline = state.PipelineState{Account: proj.Account, Stoplight: health.StoplightGrey}
	default:
		data, err := c.pipeline.FetchPipeline(ctx, proj.Pipeline.Name)
		if err != nil {
			out.Pipeline = prev.Pipeline
			out.Pipeline.FetchStatus = stale(err)
		} else {
			out.Pipeline = data
			out.Pipeline.Account = proj.Account
		}
	}

	if len(proj.Stacks) > 0 {
		switch {
		case c.cfn == nil && c.cfnErr == nil:
			// No CFN support wired up at all; nothing to report.
		case c.cfn == nil:
			out.Stacks = prev.Stacks
			out.StacksFetch = stale(noClient("CloudFormation ", proj.Account, c.cfnErr))
		default:
			names := make([]string, len(proj.Stacks))
			for j, s := range proj.Stacks {
				names[j] = s.Name
			}
			data, err := c.cfn.FetchStacks(ctx, names)
			if err != nil {
				out.Stacks = prev.Stacks
				out.StacksFetch = stale(err)
				break
			}
			out.Stacks = data
		}
	}

	if len(proj.ECS) > 0 {
		switch {
		case c.ecs == nil && c.ecsErr == nil:
			// No ECS support wired up at all; nothing to report.
		case c.ecs == nil:
			out.ECSServices = prev.ECSServices
			out.ECSFetch = stale(noClient("ECS ", proj.Account, c.ecsErr))
		default:
			// One call per cluster, and a failing cluster carries only its
			// own services forward, so it doesn't blank the ones that answered.
			var firstErr error
			for _, ecsCfg := range proj.ECS {
				data, err := c.ecs.FetchServices(ctx, ecsCfg.Cluster, ecsCfg.Services)
				if err != nil {
					if firstErr == nil {
						firstErr = err
					}
					out.ECSServices = append(out.ECSServices, servicesForCluster(prev.ECSServices, ecsCfg.Cluster)...)
					continue
				}
				out.ECSServices = append(out.ECSServices, data...)
			}
			if firstErr != nil {
				out.ECSFetch = stale(firstErr)
			}
		}
	}

	return out
}

func noClient(kind, account string, err error) error {
	return fmt.Errorf("no %sclient available for account %q: %w", kind, account, err)
}

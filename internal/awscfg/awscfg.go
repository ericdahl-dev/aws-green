// Package awscfg loads the shared AWS SDK configuration every service client
// in aws-green is built from.
//
// It exists so the retry policy is decided once. All three clients poll on the
// same tick, so a cycle arrives at AWS as a burst of calls against APIs with
// low per-account rate limits — CodePipeline's GetPipelineState and
// CloudFormation's DescribeStacks in particular. Left on the SDK default of
// three attempts with plain exponential backoff, a throttled poll simply fails
// and the dashboard shows a stale row with an opaque error.
package awscfg

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
)

// maxAttempts is deliberately above the SDK default of 3. A poll that gives up
// costs a whole cycle of visibility, and the next one is seconds away anyway,
// so it is worth waiting out a short throttle rather than blanking the row.
const maxAttempts = 5

// Load returns an AWS config for the named profile and region, using the SDK's
// adaptive retry mode. Adaptive mode keeps a client-side rate limiter that
// learns from throttle responses and slows outgoing calls before AWS has to
// reject them — the same idea as pacing against a budget, for an API that
// publishes no budget to read.
func Load(ctx context.Context, profile, region string) (aws.Config, error) {
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(region),
		awsconfig.WithRetryer(func() aws.Retryer {
			return retry.NewAdaptiveMode(func(o *retry.AdaptiveModeOptions) {
				o.StandardOptions = append(o.StandardOptions, func(s *retry.StandardOptions) {
					s.MaxAttempts = maxAttempts
				})
			})
		}),
	}
	if profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(profile))
	}

	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return aws.Config{}, fmt.Errorf("loading AWS config (profile=%q region=%q): %w", profile, region, err)
	}
	return cfg, nil
}

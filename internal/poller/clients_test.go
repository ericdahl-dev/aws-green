package poller

import (
	"errors"
	"sync"
	"testing"
)

// countingFactory builds fakes and counts builds per profile/region.
type countingFactory struct {
	mu     sync.Mutex
	builds map[string]int
	err    error
}

func (f *countingFactory) build(profile, region string) (Fetcher, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.builds == nil {
		f.builds = map[string]int{}
	}
	f.builds[profile+"/"+region]++
	if f.err != nil {
		return nil, f.err
	}
	return &fakePipelineFetcher{}, nil
}

func (f *countingFactory) count(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.builds[key]
}

// The SDK's adaptive retry limiter learns from throttling inside a client,
// so a client has to outlive a poll cycle for that learning to count.
func TestPollReusesClientsAcrossCycles(t *testing.T) {
	f := &countingFactory{}
	p := New(loadConfig(t, twoProjectConfig), f.build, nil, nil)

	pollOnce(t, p)
	pollOnce(t, p)
	pollOnce(t, p)

	if got := f.count("prod-profile/us-east-1"); got != 1 {
		t.Errorf("built the prod client %d times over 3 cycles, want 1", got)
	}
}

// A client that couldn't be built is retried next cycle — the fix (an SSO
// login, a profile added) happens outside the app.
func TestPollRetriesClientsThatFailedToBuild(t *testing.T) {
	f := &countingFactory{err: errors.New("no credentials")}
	p := New(loadConfig(t, twoProjectConfig), f.build, nil, nil)

	pollOnce(t, p)
	f.mu.Lock()
	f.err = nil
	f.mu.Unlock()
	snap := pollOnce(t, p)

	if got := f.count("prod-profile/us-east-1"); got != 2 {
		t.Errorf("built the prod client %d times, want a retry (2)", got)
	}
	if snap.Projects[0].Pipeline.Err != nil {
		t.Errorf("expected the retried client to fetch, got %v", snap.Projects[0].Pipeline.Err)
	}
}

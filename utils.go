package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/exp/slog"
)

// interface for getting data from slurm
// used for dep injection/ease of testing & for add slurmrestd support later
type SlurmFetcher interface {
	Fetch() ([]byte, error)
}

type AtomicThrottledCache struct {
	sync.Mutex
	t     time.Time
	limit float64
	cache []byte
}

// atomic fetch of either the cache or the collector
// reset & hydrate as neccesary
func (atc *AtomicThrottledCache) Fetch(slurmFetcher SlurmFetcher) ([]byte, error) {
	atc.Lock()
	defer atc.Unlock()
	if len(atc.cache) > 0 && time.Since(atc.t).Seconds() < atc.limit {
		return atc.cache, nil
	}
	slurmData, err := slurmFetcher.Fetch()
	if err != nil {
		return nil, err
	}
	atc.cache = slurmData
	atc.t = time.Now()
	return slurmData, nil
}

func NewAtomicThrottledCache() *AtomicThrottledCache {
	var limit float64 = 1
	var err error
	if lm, ok := os.LookupEnv("POLL_LIMIT"); ok {
		limit, err = strconv.ParseFloat(lm, 64)
		if err != nil {
			slog.Error("`POLL_LIMIT` env var must be a float")
			os.Exit(1)
		}
	}
	return &AtomicThrottledCache{
		t:     time.Now(),
		limit: limit,
	}
}

func track(cmd []string) (string, time.Time) {
	return strings.Join(cmd, " "), time.Now()
}

func duration(msg string, start time.Time) {
	slog.Debug(fmt.Sprintf("cmd %s took %s secs", msg, time.Since(start)))
}

// implements SlurmFetcher by fetch data from cli
type CliFetcher struct {
	args    []string
	timeout time.Duration
}

func (cf *CliFetcher) Fetch() ([]byte, error) {
	if len(cf.args) == 0 {
		return nil, errors.New("need at least 1 args")
	}
	defer duration(track(cf.args))
	cmd := exec.Command(cf.args[0], cf.args[1:]...)
	var outb, errb bytes.Buffer
	cmd.Stdout = &outb
	cmd.Stderr = &errb
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	time.AfterFunc(cf.timeout, func() {
		if err := cmd.Process.Kill(); err != nil {
			slog.Error("failed to cancel cmd: %v", cf.args)
		}
	})
	if err := cmd.Wait(); err != nil {
		return nil, err
	}
	if errb.Len() > 0 {
		return nil, errors.New(errb.String())
	}
	return outb.Bytes(), nil
}

func NewCliFetcher(args ...string) *CliFetcher {
	return &CliFetcher{
		args:    args,
		timeout: 10 * time.Second,
	}
}

// implements SlurmFetcher by pulling fixtures instead
// used exclusively for testing
type MockFetcher struct {
	fixture string
}

func (f *MockFetcher) Fetch() ([]byte, error) {
	return os.ReadFile(f.fixture)
}

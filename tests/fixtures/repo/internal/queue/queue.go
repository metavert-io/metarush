// Package queue keeps the jobs waiting to be done and runs them.
package queue

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Job is one clip to turn into a video.
type Job struct {
	Path string
	Done bool
}

// Queue holds jobs in the order they arrived.
type Queue struct {
	mu   sync.Mutex
	jobs []*Job
}

// New makes an empty queue.
func New() *Queue { return &Queue{} }

// Add puts a job at the end of the queue.
func (q *Queue) Add(j *Job) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.jobs = append(q.jobs, j)
}

// next takes the oldest job that isn't done.
func (q *Queue) next() *Job {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, j := range q.jobs {
		if !j.Done {
			return j
		}
	}
	return nil
}

// Run is the worker loop: it takes the next job, runs it, marks it done,
// and waits a moment when there is nothing to do.
func Run(ctx context.Context, q *Queue) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		j := q.next()
		if j == nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		render(j)
		j.Done = true
	}
}

// Watch adds a job for every clip that appears in dir.
func Watch(ctx context.Context, dir string, q *Queue) {
	seen := map[string]bool{}
	for ctx.Err() == nil {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			p := filepath.Join(dir, e.Name())
			if !seen[p] && filepath.Ext(p) == ".mov" {
				seen[p] = true
				q.Add(&Job{Path: p})
			}
		}
		time.Sleep(time.Second)
	}
}

func render(j *Job) { _ = j }

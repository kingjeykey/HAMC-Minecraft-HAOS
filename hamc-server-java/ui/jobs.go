package main

import (
	"fmt"
	"sync"
	"time"
)

// Job beschreibt eine länger laufende Aktion (Entpacken, Download von URL).
type Job struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Status  string    `json:"status"` // running | done | error
	Done    int64     `json:"done"`
	Total   int64     `json:"total"`
	Err     string    `json:"error,omitempty"`
	Started time.Time `json:"started"`
	Ended   time.Time `json:"ended,omitempty"`
}

type Jobs struct {
	mu   sync.Mutex
	list []*Job
	seq  int
}

func (j *Jobs) Start(name string, fn func(progress func(done, total int64)) error) string {
	j.mu.Lock()
	j.seq++
	job := &Job{ID: fmt.Sprintf("j%d", j.seq), Name: name, Status: "running", Started: time.Now()}
	j.list = append(j.list, job)
	if len(j.list) > 30 {
		j.list = j.list[len(j.list)-30:]
	}
	j.mu.Unlock()
	go func() {
		err := fn(func(done, total int64) {
			j.mu.Lock()
			job.Done, job.Total = done, total
			j.mu.Unlock()
		})
		j.mu.Lock()
		job.Ended = time.Now()
		if err != nil {
			job.Status, job.Err = "error", err.Error()
		} else {
			job.Status = "done"
		}
		j.mu.Unlock()
	}()
	return job.ID
}

func (j *Jobs) Snapshot() []Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]Job, 0, len(j.list))
	for i := len(j.list) - 1; i >= 0; i-- {
		out = append(out, *j.list[i])
	}
	return out
}

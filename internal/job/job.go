// Package job tracks in-flight compress and extract operations.
//
// Every job gets a private workspace on disk. Nothing is kept in memory except
// the small metadata record, so a multi-gigabyte archive costs the process
// almost nothing and a restart only loses bookkeeping, not disk space that a
// subsequent sweep can reclaim.
package job

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/ycyingchen/yc-7zip/internal/engine"
)

// Kind distinguishes the two directions.
type Kind string

const (
	// KindCompress packs uploaded files into an archive.
	KindCompress Kind = "compress"
	// KindExtract unpacks an uploaded archive.
	KindExtract Kind = "extract"
)

// Status is a job's lifecycle state.
type Status string

const (
	// StatusPending means accepted but not started.
	StatusPending Status = "pending"
	// StatusRunning means the engine is working.
	StatusRunning Status = "running"
	// StatusDone means the result is ready to download.
	StatusDone Status = "done"
	// StatusError means the operation failed; Message explains why.
	StatusError Status = "error"
	// StatusCancelled means the caller cancelled.
	StatusCancelled Status = "cancelled"
)

// ResultFile is one downloadable output.
type ResultFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	// Path is server-side and never serialised.
	Path string `json:"-"`
}

// OutputMode says where a job's result ends up.
type OutputMode string

const (
	// OutputDownload writes into the job workspace for the browser to fetch.
	OutputDownload OutputMode = "download"
	// OutputServer writes straight to a directory on the host, which is what a
	// NAS user expects when they act on files that are already on the NAS.
	OutputServer OutputMode = "server"
)

// Job is the public view of one operation.
type Job struct {
	ID        string       `json:"id"`
	Kind      Kind         `json:"kind"`
	Status    Status       `json:"status"`
	Progress  float64      `json:"progress"`
	Stage     string       `json:"stage,omitempty"`
	Message   string       `json:"message,omitempty"`
	CreatedAt time.Time    `json:"created_at"`
	UpdatedAt time.Time    `json:"updated_at"`
	ExpiresAt time.Time    `json:"expires_at"`
	Files     []ResultFile `json:"files,omitempty"`
	// Archive describes the archive a job is operating on.
	Archive *engine.ArchiveInfo `json:"archive,omitempty"`
	// TotalBytes is the result size, for display.
	TotalBytes int64 `json:"total_bytes"`
	// SourceNames lists the input names, for display.
	SourceNames []string `json:"source_names,omitempty"`

	// OutputMode distinguishes a downloadable result from one written to disk.
	OutputMode OutputMode `json:"output_mode,omitempty"`
	// OutputDir is the host directory a server-side job wrote to.
	OutputDir string `json:"output_dir,omitempty"`
	// ServerPaths lists what was written to the host.
	ServerPaths []string `json:"server_paths,omitempty"`
	// VolumeLabel describes a detected split volume series, if any.
	VolumeLabel string `json:"volume_label,omitempty"`

	// Workspace bookkeeping, not serialised.
	Dir    string `json:"-"`
	InDir  string `json:"-"`
	OutDir string `json:"-"`

	cancel context.CancelFunc
}

// Manager owns the job table and the workspace root.
type Manager struct {
	root string
	ttl  time.Duration

	mu   sync.RWMutex
	jobs map[string]*Job
}

// NewManager creates the workspace root and starts the reaper.
func NewManager(root string, ttl time.Duration) (*Manager, error) {
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, err
	}
	m := &Manager{root: root, ttl: ttl, jobs: map[string]*Job{}}
	go m.reapLoop()
	return m, nil
}

// Root is the workspace root, exported so the server can report disk use.
func (m *Manager) Root() string { return m.root }

// Create reserves a workspace and returns a pending job.
func (m *Manager) Create(kind Kind) (*Job, error) {
	id, err := newID()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(m.root, id)
	inDir := filepath.Join(dir, "in")
	outDir := filepath.Join(dir, "out")
	tmpDir := filepath.Join(dir, "tmp")
	for _, d := range []string{inDir, outDir, tmpDir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return nil, err
		}
	}
	now := time.Now()
	j := &Job{
		ID:        id,
		Kind:      kind,
		Status:    StatusPending,
		CreatedAt: now,
		UpdatedAt: now,
		ExpiresAt: now.Add(m.ttl),
		Dir:       dir,
		InDir:     inDir,
		OutDir:    outDir,
	}
	m.mu.Lock()
	m.jobs[id] = j
	m.mu.Unlock()
	return j, nil
}

// Get returns a job snapshot by id.
func (m *Manager) Get(id string) (*Job, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	j, ok := m.jobs[id]
	return j, ok
}

// Update mutates a job under the lock through fn.
func (m *Manager) Update(id string, fn func(*Job)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if j, ok := m.jobs[id]; ok {
		fn(j)
		j.UpdatedAt = time.Now()
	}
}

// Fail records a terminal error state.
func (m *Manager) Fail(id string, err error) {
	m.Update(id, func(j *Job) {
		if j.Status == StatusCancelled {
			return
		}
		j.Status = StatusError
		j.Message = err.Error()
		j.cancel = nil
	})
}

// Begin marks a job running and installs its cancel function.
func (m *Manager) Begin(id string, cancel context.CancelFunc) {
	m.Update(id, func(j *Job) {
		j.Status = StatusRunning
		j.Stage = "starting"
		j.cancel = cancel
	})
}

// Cancel stops a running job. It reports false when the job already finished.
func (m *Manager) Cancel(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return false
	}
	if j.Status == StatusDone || j.Status == StatusError || j.Status == StatusCancelled {
		return false
	}
	if j.cancel != nil {
		j.cancel()
	}
	j.Status = StatusCancelled
	j.Message = "已取消"
	j.UpdatedAt = time.Now()
	return true
}

// Delete cancels and removes a job along with its workspace.
func (m *Manager) Delete(id string) bool {
	m.mu.Lock()
	j, ok := m.jobs[id]
	if !ok {
		m.mu.Unlock()
		return false
	}
	if j.cancel != nil {
		j.cancel()
	}
	delete(m.jobs, id)
	m.mu.Unlock()

	go func() {
		// Give the engine a moment to release file handles before tearing the
		// tree down, otherwise Windows refuses to delete open files.
		time.Sleep(300 * time.Millisecond)
		_ = RemoveAll(j.Dir)
	}()
	return true
}

// reapLoop deletes expired workspaces so a long-lived server does not fill the
// disk with abandoned uploads.
func (m *Manager) reapLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		var dead []string
		m.mu.RLock()
		for id, j := range m.jobs {
			if now.After(j.ExpiresAt) && j.Status != StatusRunning {
				dead = append(dead, id)
			}
		}
		m.mu.RUnlock()
		for _, id := range dead {
			m.Delete(id)
		}
	}
}

// Reap removes expired jobs immediately and returns how many were dropped.
func (m *Manager) Reap() int {
	now := time.Now()
	var dead []string
	m.mu.RLock()
	for id, j := range m.jobs {
		if now.After(j.ExpiresAt) && j.Status != StatusRunning {
			dead = append(dead, id)
		}
	}
	m.mu.RUnlock()
	for _, id := range dead {
		m.Delete(id)
	}
	return len(dead)
}

// CollectOutputs walks a job's output directory and fills Files. It is called
// once an operation reaches a terminal state.
func (m *Manager) CollectOutputs(id string) error {
	var files []ResultFile
	var total int64

	m.mu.RLock()
	j, ok := m.jobs[id]
	m.mu.RUnlock()
	if !ok {
		return errors.New("任务不存在")
	}

	err := filepath.Walk(j.OutDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(j.OutDir, p)
		if relErr != nil {
			rel = info.Name()
		}
		files = append(files, ResultFile{
			Name: filepath.ToSlash(rel),
			Size: info.Size(),
			Path: p,
		})
		total += info.Size()
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(files, func(a, b int) bool { return files[a].Name < files[b].Name })

	m.Update(id, func(jb *Job) {
		jb.Files = files
		jb.TotalBytes = total
	})
	return nil
}

// Finish marks a job complete.
func (m *Manager) Finish(id string) {
	m.Update(id, func(j *Job) {
		j.Status = StatusDone
		j.Progress = 100
		j.Stage = "done"
		j.cancel = nil
	})
}

// newID returns a 128-bit URL-safe identifier. It must be unguessable: the id
// is the only credential protecting a job's uploaded data.
func newID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

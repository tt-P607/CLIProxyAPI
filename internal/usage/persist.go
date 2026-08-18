// Package usage provides usage tracking and logging functionality for the CLI Proxy API server.
package usage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
)

var (
	persistPath string
	persistMu   sync.Mutex
	dirty       atomic.Bool
	loopOnce    sync.Once
)

// SetPersistPath sets the filepath where usage statistics should be persisted.
// If not empty, it will try to load stats from this path at startup.
func SetPersistPath(path string) {
	persistMu.Lock()
	persistPath = path
	persistMu.Unlock()

	// Load existing stats if available
	if path != "" {
		if err := defaultRequestStatistics.LoadFromFile(path); err != nil && !os.IsNotExist(err) {
			log.Warnf("failed to load usage statistics from %s: %v", path, err)
		}

		// Start the periodic persist loop
		loopOnce.Do(func() {
			go startPersistLoop()
		})
	}
}

func startPersistLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		persistMu.Lock()
		path := persistPath
		persistMu.Unlock()
		if path == "" {
			continue
		}
		if dirty.Swap(false) {
			if errSave := defaultRequestStatistics.SaveToFile(path); errSave != nil {
				log.Warnf("failed to auto-persist usage statistics: %v", errSave)
			}
		}
	}
}

// triggerPersist is called by LoggerPlugin to flag that data has changed.
// Instead of spawning a goroutine and writing immediately, it flags dirty to let the loop handle it.
func triggerPersist(s *RequestStatistics) {
	dirty.Store(true)
}

// Sync force persists current statistics to the configured path synchronously.
func Sync() error {
	persistMu.Lock()
	path := persistPath
	persistMu.Unlock()
	if path == "" {
		return nil
	}
	dirty.Store(false)
	return defaultRequestStatistics.SaveToFile(path)
}

// ResetStats clears the shared in-memory statistics and synchronizes the empty state to disk.
func ResetStats(s *RequestStatistics) error {
	s.Reset()
	return Sync()
}

// LoadFromFile loads the request statistics from a JSON file.
func (s *RequestStatistics) LoadFromFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var snapshot StatisticsSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return fmt.Errorf("parse stats: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.totalRequests = snapshot.TotalRequests
	s.successCount = snapshot.SuccessCount
	s.failureCount = snapshot.FailureCount
	s.totalTokens = snapshot.TotalTokens

	s.apis = make(map[string]*apiStats)
	for apiName, apiSnap := range snapshot.APIs {
		astats := &apiStats{
			TotalRequests: apiSnap.TotalRequests,
			TotalTokens:   apiSnap.TotalTokens,
			Models:        make(map[string]*modelStats),
		}
		for modelName, modelSnap := range apiSnap.Models {
			mstats := &modelStats{
				TotalRequests: modelSnap.TotalRequests,
				TotalTokens:   modelSnap.TotalTokens,
				Details:       modelSnap.Details,
			}
			astats.Models[modelName] = mstats
		}
		s.apis[apiName] = astats
	}

	s.requestsByDay = make(map[string]int64)
	for k, v := range snapshot.RequestsByDay {
		s.requestsByDay[k] = v
	}

	s.requestsByHour = make(map[int]int64)
	for k, v := range snapshot.RequestsByHour {
		var hr int
		if _, errParse := fmt.Sscanf(k, "%d", &hr); errParse == nil {
			s.requestsByHour[hr] = v
		}
	}

	s.tokensByDay = make(map[string]int64)
	for k, v := range snapshot.TokensByDay {
		s.tokensByDay[k] = v
	}

	s.tokensByHour = make(map[int]int64)
	for k, v := range snapshot.TokensByHour {
		var hr int
		if _, errParse := fmt.Sscanf(k, "%d", &hr); errParse == nil {
			s.tokensByHour[hr] = v
		}
	}

	log.Infof("loaded usage statistics from %s (requests=%d, tokens=%d)", path, snapshot.TotalRequests, snapshot.TotalTokens)
	return nil
}

// SaveToFile persists the request statistics to a JSON file atomically.
func (s *RequestStatistics) SaveToFile(path string) error {
	snapshot := s.Snapshot()
	data, errMarshal := json.MarshalIndent(snapshot, "", "  ")
	if errMarshal != nil {
		return errMarshal
	}

	dir := filepath.Dir(path)
	if errMkdir := os.MkdirAll(dir, 0o700); errMkdir != nil {
		return errMkdir
	}

	tmpFile, errCreate := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if errCreate != nil {
		return errCreate
	}
	tmpName := tmpFile.Name()

	if _, errWrite := tmpFile.Write(data); errWrite != nil {
		if errClose := tmpFile.Close(); errClose != nil {
			log.Errorf("failed to close temp file %s: %v", tmpName, errClose)
		}
		_ = os.Remove(tmpName)
		return errWrite
	}
	if errClose := tmpFile.Close(); errClose != nil {
		_ = os.Remove(tmpName)
		return errClose
	}

	if errRename := os.Rename(tmpName, path); errRename != nil {
		_ = os.Remove(tmpName)
		return errRename
	}

	return nil
}

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// RequestStatsRecord is a persisted snapshot of per-credential request counters
// and runtime health state.
//
// Buckets are stored with their absolute bucket identifier so restoring never
// has to guess the original date from a formatted label. Health fields are
// persisted alongside counters so a restart preserves the visible credential
// health (status message, availability, quota) exactly like request totals.
type RequestStatsRecord struct {
	Provider  string                     `json:"provider,omitempty"`
	AuthID    string                     `json:"auth_id"`
	AuthFile  string                     `json:"-"`
	Success   int64                      `json:"success"`
	Failed    int64                      `json:"failed"`
	Buckets   []RequestStatsBucketRecord `json:"buckets,omitempty"`
	UpdatedAt time.Time                  `json:"updated_at"`

	// HealthSnapshot captures the credential runtime health so it can be
	// restored alongside the counters after a restart.
	Status         Status                 `json:"status,omitempty"`
	StatusMessage  string                 `json:"status_message,omitempty"`
	Unavailable    bool                   `json:"unavailable,omitempty"`
	NextRetryAfter time.Time              `json:"next_retry_after,omitempty"`
	Quota          QuotaState             `json:"quota,omitempty"`
	LastError      *Error                 `json:"last_error,omitempty"`
	ModelStates    map[string]*ModelState `json:"model_states,omitempty"`
}

// RequestStatsBucketRecord persists a single recent-request bucket.
type RequestStatsBucketRecord struct {
	BucketID int64 `json:"bucket_id"`
	Success  int64 `json:"success"`
	Failed   int64 `json:"failed"`
}

// RequestStatsStore persists request statistics independently from auth tokens.
type RequestStatsStore interface {
	Load(context.Context) ([]RequestStatsRecord, error)
	Save(context.Context, []RequestStatsRecord) error
}

type requestStatsFile struct {
	Version   int                  `json:"version"`
	AuthID    string               `json:"auth_id,omitempty"`
	Provider  string               `json:"provider,omitempty"`
	UpdatedAt time.Time            `json:"updated_at"`
	Records   []RequestStatsRecord `json:"records"`
}

// FileRequestStatsStore stores request statistics as one .rqs file per auth.
type FileRequestStatsStore struct {
	mu      sync.Mutex
	dir     string
	authDir string
}

// NewFileRequestStatsStore creates a file-backed request statistics store rooted at dir.
func NewFileRequestStatsStore(dir string) *FileRequestStatsStore {
	return NewFileRequestStatsStoreWithAuthDir(dir, "")
}

// NewFileRequestStatsStoreWithAuthDir creates a store and derives per-auth .rqs
// paths from auth files relative to authDir when possible.
func NewFileRequestStatsStoreWithAuthDir(dir, authDir string) *FileRequestStatsStore {
	return &FileRequestStatsStore{
		dir:     strings.TrimSpace(dir),
		authDir: strings.TrimSpace(authDir),
	}
}

// Load reads all request statistics files. A missing directory is treated as empty state.
func (s *FileRequestStatsStore) Load(ctx context.Context) ([]RequestStatsRecord, error) {
	if s == nil || s.dir == "" {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errCtx := ctx.Err(); errCtx != nil {
		return nil, errCtx
	}

	records := make([]RequestStatsRecord, 0)
	errWalk := filepath.WalkDir(s.dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if entry == nil || entry.IsDir() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(entry.Name()), requestStatsFileExt) {
			return nil
		}
		fileRecords, errRead := readRequestStatsFile(ctx, path)
		if errRead != nil {
			return errRead
		}
		records = append(records, fileRecords...)
		return nil
	})
	if errWalk != nil {
		if errors.Is(errWalk, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read request stats directory: %w", errWalk)
	}
	return records, nil
}

func readRequestStatsFile(ctx context.Context, path string) ([]RequestStatsRecord, error) {
	if errCtx := ctx.Err(); errCtx != nil {
		return nil, errCtx
	}
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		if errors.Is(errRead, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read request stats %s: %w", path, errRead)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, nil
	}
	var envelope requestStatsFile
	if errUnmarshal := json.Unmarshal(data, &envelope); errUnmarshal != nil {
		return nil, fmt.Errorf("parse request stats %s: %w", path, errUnmarshal)
	}
	return envelope.Records, nil
}

// Save atomically writes one request statistics file per auth and removes stale files.
func (s *FileRequestStatsStore) Save(ctx context.Context, records []RequestStatsRecord) error {
	if s == nil || s.dir == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errCtx := ctx.Err(); errCtx != nil {
		return errCtx
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	groups := make(map[string][]RequestStatsRecord)
	for _, record := range records {
		if strings.TrimSpace(record.AuthID) == "" {
			continue
		}
		path, errPath := s.statePath(record)
		if errPath != nil {
			return errPath
		}
		if path == "" {
			continue
		}
		groups[path] = append(groups[path], record)
	}

	if len(groups) == 0 {
		return s.removeAllStateFiles(ctx)
	}
	if errMkdir := os.MkdirAll(s.dir, 0o700); errMkdir != nil {
		return fmt.Errorf("create request stats directory: %w", errMkdir)
	}

	desired := make(map[string]struct{}, len(groups))
	for path, groupedRecords := range groups {
		if errSave := writeRequestStatsGroup(ctx, path, groupedRecords); errSave != nil {
			return errSave
		}
		desired[filepath.Clean(path)] = struct{}{}
	}
	return s.removeStaleStateFiles(ctx, desired)
}

func writeRequestStatsGroup(ctx context.Context, path string, records []RequestStatsRecord) error {
	if errCtx := ctx.Err(); errCtx != nil {
		return errCtx
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].AuthID < records[j].AuthID
	})
	envelope := requestStatsFile{
		Version:   1,
		UpdatedAt: time.Now().UTC(),
		Records:   records,
	}
	if len(records) > 0 {
		envelope.AuthID = records[0].AuthID
		envelope.Provider = records[0].Provider
	}
	data, errMarshal := json.MarshalIndent(envelope, "", "  ")
	if errMarshal != nil {
		return fmt.Errorf("marshal request stats: %w", errMarshal)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if errMkdir := os.MkdirAll(dir, 0o700); errMkdir != nil {
		return fmt.Errorf("create request stats directory: %w", errMkdir)
	}
	tmp, errTmp := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if errTmp != nil {
		return fmt.Errorf("create request stats temp file: %w", errTmp)
	}
	tmpName := tmp.Name()
	if _, errWrite := tmp.Write(data); errWrite != nil {
		if errClose := tmp.Close(); errClose != nil {
			return fmt.Errorf("close request stats temp file: %w", errClose)
		}
		if errRemove := os.Remove(tmpName); errRemove != nil {
			return fmt.Errorf("remove request stats temp file: %w", errRemove)
		}
		return fmt.Errorf("write request stats: %w", errWrite)
	}
	if errClose := tmp.Close(); errClose != nil {
		if errRemove := os.Remove(tmpName); errRemove != nil {
			return fmt.Errorf("remove request stats temp file: %w", errRemove)
		}
		return fmt.Errorf("close request stats temp file: %w", errClose)
	}
	if errRename := os.Rename(tmpName, path); errRename != nil {
		if errRemove := os.Remove(tmpName); errRemove != nil {
			return fmt.Errorf("remove request stats temp file: %w", errRemove)
		}
		return fmt.Errorf("persist request stats: %w", errRename)
	}
	return nil
}

func (s *FileRequestStatsStore) removeAllStateFiles(ctx context.Context) error {
	return s.removeStaleStateFiles(ctx, nil)
}

func (s *FileRequestStatsStore) removeStaleStateFiles(ctx context.Context, desired map[string]struct{}) error {
	if errCtx := ctx.Err(); errCtx != nil {
		return errCtx
	}
	errWalk := filepath.WalkDir(s.dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if entry == nil || entry.IsDir() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(entry.Name()), requestStatsFileExt) {
			return nil
		}
		if _, keep := desired[filepath.Clean(path)]; keep {
			return nil
		}
		if errRemove := os.Remove(path); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
			return fmt.Errorf("remove stale request stats %s: %w", path, errRemove)
		}
		return nil
	})
	if errWalk != nil && !errors.Is(errWalk, os.ErrNotExist) {
		return errWalk
	}
	return nil
}

const requestStatsFileExt = ".rqs"

var requestStatsFileNameUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// statePath resolves the on-disk path for a record, mirroring the auth file
// layout when the auth file is known so statistics live next to their credential.
func (s *FileRequestStatsStore) statePath(record RequestStatsRecord) (string, error) {
	relative := s.relativeStateName(record)
	if relative == "" {
		return "", nil
	}
	return filepath.Join(s.dir, relative), nil
}

func (s *FileRequestStatsStore) relativeStateName(record RequestStatsRecord) string {
	authFile := strings.TrimSpace(record.AuthFile)
	if authFile != "" {
		if name := s.stateNameFromAuthFile(authFile); name != "" {
			return name
		}
	}
	authID := strings.TrimSpace(record.AuthID)
	if authID == "" {
		return ""
	}
	sanitized := requestStatsFileNameUnsafe.ReplaceAllString(authID, "_")
	sanitized = strings.Trim(sanitized, "._-")
	if sanitized == "" {
		return ""
	}
	return sanitized + requestStatsFileExt
}

func (s *FileRequestStatsStore) stateNameFromAuthFile(authFile string) string {
	candidate := authFile
	if s.authDir != "" && filepath.IsAbs(authFile) {
		if rel, errRel := filepath.Rel(s.authDir, authFile); errRel == nil && !strings.HasPrefix(rel, "..") {
			candidate = rel
		} else {
			candidate = filepath.Base(authFile)
		}
	} else if filepath.IsAbs(authFile) {
		candidate = filepath.Base(authFile)
	}
	candidate = filepath.Clean(candidate)
	if candidate == "." || candidate == string(filepath.Separator) {
		return ""
	}
	if strings.HasPrefix(candidate, "..") {
		return ""
	}
	ext := filepath.Ext(candidate)
	if ext != "" {
		candidate = strings.TrimSuffix(candidate, ext)
	}
	if candidate == "" {
		return ""
	}
	return candidate + requestStatsFileExt
}

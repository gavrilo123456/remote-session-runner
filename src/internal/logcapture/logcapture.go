// Package logcapture bounds one child's combined stdout/stderr capture without
// writing diagnostics to the host journal or launchd's default log path.
package logcapture

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	MiB                uint64 = 1024 * 1024
	counterRecordBytes        = 24
	counterFileBytes          = 2 * counterRecordBytes
)

var (
	ErrInvalidLogPath   = errors.New("invalid log capture path")
	ErrUnsafeLogState   = errors.New("unsafe log capture state")
	ErrInjectedCrash    = errors.New("injected rotation interruption")
	ErrInvalidLogLimits = errors.New("invalid log capture limits")
)

type Limits struct {
	ActiveFileBytes  uint64
	RotatedFileBytes uint64
	MaxRotatedFiles  int
	TempFileBytes    uint64
}

func DefaultLimits() Limits {
	return Limits{ActiveFileBytes: 64 * MiB, RotatedFileBytes: 64 * MiB, MaxRotatedFiles: 7, TempFileBytes: 64 * MiB}
}

func (l Limits) TotalBytes() uint64 {
	if l.MaxRotatedFiles < 0 {
		return math.MaxUint64
	}
	rotations := uint64(l.MaxRotatedFiles)
	if rotations != 0 && l.RotatedFileBytes > math.MaxUint64/rotations {
		return math.MaxUint64
	}
	total := l.RotatedFileBytes * rotations
	if l.ActiveFileBytes > math.MaxUint64-total {
		return math.MaxUint64
	}
	total += l.ActiveFileBytes
	if l.TempFileBytes > math.MaxUint64-total {
		return math.MaxUint64
	}
	return total + l.TempFileBytes
}

func (l Limits) validate() error {
	if l.ActiveFileBytes == 0 || l.RotatedFileBytes == 0 || l.TempFileBytes == 0 || l.MaxRotatedFiles < 1 || l.MaxRotatedFiles > 7 ||
		l.TempFileBytes < l.ActiveFileBytes || l.RotatedFileBytes < l.ActiveFileBytes || l.RotatedFileBytes > math.MaxUint64/uint64(l.MaxRotatedFiles) {
		return ErrInvalidLogLimits
	}
	rotatedBytes := l.RotatedFileBytes * uint64(l.MaxRotatedFiles)
	if l.ActiveFileBytes > math.MaxUint64-rotatedBytes || l.TempFileBytes > math.MaxUint64-l.ActiveFileBytes-rotatedBytes {
		return ErrInvalidLogLimits
	}
	if l.TotalBytes() > 576*MiB {
		return ErrInvalidLogLimits
	}
	return nil
}

type Status struct {
	DroppedLogBytes uint64 `json:"dropped_log_bytes"`
}

type Store struct {
	mu              sync.Mutex
	path            string
	dir             string
	base            string
	counterPath     string
	counter         *os.File
	limits          Limits
	dropped         uint64
	generation      uint64
	closed          bool
	writeFile       func(*os.File, []byte) (int, error)
	afterTempRename func() error
}

func Open(logPath string) (*Store, error) {
	return openWithLimits(logPath, DefaultLimits())
}

func openWithLimits(logPath string, limits Limits) (*Store, error) {
	if err := limits.validate(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(logPath) || filepath.Clean(logPath) != logPath || filepath.Base(logPath) == "." || filepath.Base(logPath) == string(filepath.Separator) {
		return nil, ErrInvalidLogPath
	}
	dir := filepath.Dir(logPath)
	dirInfo, err := os.Lstat(dir)
	if err != nil || !dirInfo.IsDir() || dirInfo.Mode()&os.ModeSymlink != 0 {
		return nil, ErrInvalidLogPath
	}
	store := &Store{
		path: logPath, dir: dir, base: filepath.Base(logPath), counterPath: logPath + ".dropped_log_bytes",
		limits: limits, writeFile: (*os.File).Write,
	}
	if err := store.validateKnownFiles(); err != nil {
		return nil, err
	}
	if err := store.openCounter(); err != nil {
		return nil, err
	}
	if err := store.recoverRotation(); err != nil {
		_ = store.counter.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, ErrUnsafeLogState
	}
	remaining := data
	for len(remaining) > 0 {
		activeSize, err := s.activeSize()
		if err != nil {
			return 0, err
		}
		if activeSize == s.limits.ActiveFileBytes {
			if err := s.rotate(); err != nil {
				if errors.Is(err, ErrInjectedCrash) || errors.Is(err, ErrUnsafeLogState) {
					return 0, err
				}
				if dropErr := s.addDropped(uint64(len(remaining))); dropErr != nil {
					return 0, dropErr
				}
				return len(data), nil
			}
			continue
		}
		capacity := s.limits.ActiveFileBytes - activeSize
		chunkSize := uint64(len(remaining))
		if chunkSize > capacity {
			chunkSize = capacity
		}
		file, err := os.OpenFile(s.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND|syscall.O_NOFOLLOW, 0o600)
		if err != nil {
			if dropErr := s.addDropped(uint64(len(remaining))); dropErr != nil {
				return 0, dropErr
			}
			return len(data), nil
		}
		if info, statErr := file.Stat(); statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() < 0 || uint64(info.Size()) != activeSize {
			_ = file.Close()
			return 0, ErrUnsafeLogState
		}
		chunk := remaining[:int(chunkSize)]
		n, writeErr := s.writeFile(file, chunk)
		closeErr := file.Close()
		if n < 0 || n > len(chunk) {
			return 0, ErrUnsafeLogState
		}
		remaining = remaining[n:]
		if writeErr != nil || closeErr != nil || n != len(chunk) {
			if dropErr := s.addDropped(uint64(len(remaining))); dropErr != nil {
				return 0, dropErr
			}
			return len(data), nil
		}
	}
	return len(data), nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if active, err := os.OpenFile(s.path, os.O_WRONLY|syscall.O_NOFOLLOW, 0); err == nil {
		if syncErr := active.Sync(); syncErr != nil {
			_ = active.Close()
			_ = s.counter.Close()
			return syncErr
		}
		if closeErr := active.Close(); closeErr != nil {
			_ = s.counter.Close()
			return closeErr
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		_ = s.counter.Close()
		return err
	}
	if err := s.counter.Sync(); err != nil {
		_ = s.counter.Close()
		return err
	}
	return s.counter.Close()
}

func ReadStatus(logPath string) (Status, error) {
	if !filepath.IsAbs(logPath) || filepath.Clean(logPath) != logPath {
		return Status{}, ErrInvalidLogPath
	}
	path := logPath + ".dropped_log_bytes"
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 || info.Size() != counterFileBytes {
		return Status{}, ErrUnsafeLogState
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return Status{}, ErrUnsafeLogState
	}
	defer file.Close()
	status, _, err := readCounter(file)
	return status, err
}

func (s *Store) activeSize() (uint64, error) {
	info, err := os.Lstat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 || info.Size() < 0 {
		return 0, ErrUnsafeLogState
	}
	size := uint64(info.Size())
	if size > s.limits.ActiveFileBytes {
		return 0, ErrUnsafeLogState
	}
	return size, nil
}

func (s *Store) rotate() error {
	activeSize, err := s.activeSize()
	if err != nil || activeSize == 0 {
		return ErrUnsafeLogState
	}
	tempPath := s.path + ".rotate.tmp"
	if _, err := os.Lstat(tempPath); err == nil || !errors.Is(err, os.ErrNotExist) {
		return ErrUnsafeLogState
	}
	rotated, err := s.rotatedFiles()
	if err != nil {
		return err
	}
	if len(rotated) >= s.limits.MaxRotatedFiles {
		if err := os.Remove(rotated[0].path); err != nil {
			return err
		}
		if err := syncDirectory(s.dir); err != nil {
			return err
		}
	}
	active, err := os.OpenFile(s.path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	if err := active.Sync(); err != nil {
		_ = active.Close()
		return err
	}
	if err := active.Close(); err != nil {
		return err
	}
	if err := os.Rename(s.path, tempPath); err != nil {
		return err
	}
	if err := syncDirectory(s.dir); err != nil {
		return fmt.Errorf("%w: sync after active-to-temp rename: %v", ErrUnsafeLogState, err)
	}
	if s.afterTempRename != nil {
		if err := s.afterTempRename(); err != nil {
			return err
		}
	}
	if err := s.finishTemp(tempPath); err != nil {
		return fmt.Errorf("%w: finish rotated temporary file: %v", ErrUnsafeLogState, err)
	}
	return nil
}

func (s *Store) recoverRotation() error {
	tempPath := s.path + ".rotate.tmp"
	info, err := os.Lstat(tempPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 ||
		info.Size() <= 0 || uint64(info.Size()) > s.limits.TempFileBytes {
		return ErrUnsafeLogState
	}
	if _, err := os.Lstat(s.path); err == nil || !errors.Is(err, os.ErrNotExist) {
		return ErrUnsafeLogState
	}
	rotated, err := s.rotatedFiles()
	if err != nil {
		return err
	}
	if len(rotated) >= s.limits.MaxRotatedFiles {
		if err := os.Remove(rotated[0].path); err != nil {
			return err
		}
		if err := syncDirectory(s.dir); err != nil {
			return err
		}
	}
	return s.finishTemp(tempPath)
}

func (s *Store) finishTemp(tempPath string) error {
	info, err := os.Lstat(tempPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 || info.Size() < 0 || uint64(info.Size()) > s.limits.RotatedFileBytes {
		return ErrUnsafeLogState
	}
	rotated, err := s.rotatedFiles()
	if err != nil {
		return err
	}
	next := uint64(1)
	if len(rotated) != 0 {
		if rotated[len(rotated)-1].sequence == math.MaxUint64 {
			return ErrUnsafeLogState
		}
		next = rotated[len(rotated)-1].sequence + 1
	}
	archive := fmt.Sprintf("%s.rot.%020d", s.path, next)
	if err := os.Rename(tempPath, archive); err != nil {
		return err
	}
	return syncDirectory(s.dir)
}

type rotatedFile struct {
	path     string
	sequence uint64
	size     uint64
}

func (s *Store) rotatedFiles() ([]rotatedFile, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, ErrUnsafeLogState
	}
	prefix := s.base + ".rot."
	files := make([]rotatedFile, 0)
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		suffix := strings.TrimPrefix(name, prefix)
		sequence, err := strconv.ParseUint(suffix, 10, 64)
		if err != nil || sequence == 0 {
			return nil, ErrUnsafeLogState
		}
		path := filepath.Join(s.dir, name)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 || info.Size() < 0 || uint64(info.Size()) > s.limits.RotatedFileBytes {
			return nil, ErrUnsafeLogState
		}
		files = append(files, rotatedFile{path: path, sequence: sequence, size: uint64(info.Size())})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].sequence < files[j].sequence })
	if len(files) > s.limits.MaxRotatedFiles {
		return nil, ErrUnsafeLogState
	}
	for index := 1; index < len(files); index++ {
		if files[index-1].sequence == files[index].sequence {
			return nil, ErrUnsafeLogState
		}
	}
	return files, nil
}

func (s *Store) validateKnownFiles() error {
	if _, err := s.activeSize(); err != nil {
		return err
	}
	if _, err := s.rotatedFiles(); err != nil {
		return err
	}
	tempPath := s.path + ".rotate.tmp"
	if info, err := os.Lstat(tempPath); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 || info.Size() < 0 || uint64(info.Size()) > s.limits.TempFileBytes {
			return ErrUnsafeLogState
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrUnsafeLogState
	}
	return nil
}

func (s *Store) openCounter() error {
	info, err := os.Lstat(s.counterPath)
	created := errors.Is(err, os.ErrNotExist)
	if err != nil && !created {
		return ErrUnsafeLogState
	}
	if created {
		s.counter, err = os.OpenFile(s.counterPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return ErrUnsafeLogState
		}
		initial := encodeCounter(0, 0)
		if _, err := s.counter.WriteAt(initial, 0); err != nil {
			_ = s.counter.Close()
			return ErrUnsafeLogState
		}
		if _, err := s.counter.WriteAt(initial, counterRecordBytes); err != nil || s.counter.Sync() != nil || syncDirectory(s.dir) != nil {
			_ = s.counter.Close()
			return ErrUnsafeLogState
		}
		s.dropped, s.generation = 0, 0
		return nil
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 || info.Size() != counterFileBytes {
		return ErrUnsafeLogState
	}
	s.counter, err = os.OpenFile(s.counterPath, os.O_RDWR|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return ErrUnsafeLogState
	}
	status, generation, err := readCounter(s.counter)
	if err != nil {
		_ = s.counter.Close()
		return err
	}
	s.dropped, s.generation = status.DroppedLogBytes, generation
	return nil
}

func (s *Store) addDropped(amount uint64) error {
	if amount == 0 {
		return nil
	}
	if amount > math.MaxUint64-s.dropped {
		return ErrUnsafeLogState
	}
	next := s.dropped + amount
	generation := s.generation + 1
	if generation == 0 {
		return ErrUnsafeLogState
	}
	data := encodeCounter(generation, next)
	offset := int64(generation%2) * counterRecordBytes
	n, err := s.counter.WriteAt(data, offset)
	if err != nil || n != counterRecordBytes || s.counter.Sync() != nil {
		return ErrUnsafeLogState
	}
	s.dropped, s.generation = next, generation
	return nil
}

func encodeCounter(generation, dropped uint64) []byte {
	data := make([]byte, counterRecordBytes)
	copy(data[:4], []byte{'R', 'L', 'D', '1'})
	binary.BigEndian.PutUint64(data[4:12], generation)
	binary.BigEndian.PutUint64(data[12:20], dropped)
	binary.BigEndian.PutUint32(data[20:24], crc32.ChecksumIEEE(data[:20]))
	return data
}

func readCounter(file *os.File) (Status, uint64, error) {
	info, err := file.Stat()
	if err != nil || info.Size() != counterFileBytes {
		return Status{}, 0, ErrUnsafeLogState
	}
	data := make([]byte, counterFileBytes)
	if _, err := file.ReadAt(data, 0); err != nil && !errors.Is(err, io.EOF) {
		return Status{}, 0, ErrUnsafeLogState
	}
	var bestGeneration, bestDropped uint64
	found := false
	for offset := int64(0); offset < counterFileBytes; offset += counterRecordBytes {
		record := data[offset : offset+counterRecordBytes]
		if string(record[:4]) != "RLD1" || binary.BigEndian.Uint32(record[20:24]) != crc32.ChecksumIEEE(record[:20]) {
			continue
		}
		generation, dropped := binary.BigEndian.Uint64(record[4:12]), binary.BigEndian.Uint64(record[12:20])
		if !found || generation > bestGeneration {
			bestGeneration, bestDropped, found = generation, dropped, true
		}
	}
	if !found {
		return Status{}, 0, ErrUnsafeLogState
	}
	return Status{DroppedLogBytes: bestDropped}, bestGeneration, nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func RunCLI(args []string, stdout, stderr io.Writer) int {
	return RunCLIContext(context.Background(), args, stdout, stderr)
}

func RunCLIContext(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	set := flag.NewFlagSet("runner-log-capture", flag.ContinueOnError)
	set.SetOutput(stderr)
	logPath := set.String("log-path", "", "fixed active log file path")
	statusOnly := set.Bool("status", false, "print the durable dropped byte counter")
	if err := set.Parse(args); err != nil || *logPath == "" {
		return 78
	}
	if *statusOnly {
		if set.NArg() != 0 {
			return 78
		}
		status, err := ReadStatus(*logPath)
		if err != nil {
			return 78
		}
		if json.NewEncoder(stdout).Encode(status) != nil {
			return 78
		}
		return 0
	}
	if set.NArg() < 1 {
		return 78
	}
	return RunChild(ctx, *logPath, set.Args())
}

func RunChild(ctx context.Context, logPath string, argv []string) int {
	if len(argv) == 0 || !filepath.IsAbs(argv[0]) || filepath.Clean(argv[0]) != argv[0] {
		return 78
	}
	store, err := Open(logPath)
	if err != nil {
		return 78
	}
	closed := false
	defer func() {
		if !closed {
			_ = store.Close()
		}
	}()
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var killOnce sync.Once
	writer := childLogWriter{store: store, kill: func() {
		killOnce.Do(func() {
			if command.Process != nil {
				_ = command.Process.Kill()
			}
		})
	}}
	command.Stdout = writer
	command.Stderr = writer
	// A child may exit while a descendant still holds its output descriptors.
	// Bound how long os/exec waits for those copies, then fail closed.
	command.WaitDelay = 5 * time.Second
	waitErr := command.Run()
	if err := store.Close(); err != nil {
		return 78
	}
	closed = true
	if waitErr == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		return exitErr.ExitCode()
	}
	return 78
}

type childLogWriter struct {
	store *Store
	kill  func()
}

func (w childLogWriter) Write(data []byte) (int, error) {
	n, err := w.store.Write(data)
	if err != nil {
		w.kill()
	}
	return n, err
}

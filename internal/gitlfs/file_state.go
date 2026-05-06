package gitlfs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func replacePointerFile(src, dst string, mode fs.FileMode) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open lfs temp object: %w", err)
	}
	defer srcFile.Close()

	tmpDst, err := os.CreateTemp(filepath.Dir(dst), ".gomall-lfs-*")
	if err != nil {
		return fmt.Errorf("create destination temp file: %w", err)
	}
	tmpDstPath := tmpDst.Name()

	if _, err := io.Copy(tmpDst, srcFile); err != nil {
		_ = tmpDst.Close()
		_ = os.Remove(tmpDstPath)
		return fmt.Errorf("write destination temp file: %w", err)
	}
	if err := tmpDst.Chmod(mode.Perm()); err != nil {
		_ = tmpDst.Close()
		_ = os.Remove(tmpDstPath)
		return fmt.Errorf("chmod destination temp file: %w", err)
	}
	if err := tmpDst.Close(); err != nil {
		_ = os.Remove(tmpDstPath)
		return fmt.Errorf("close destination temp file: %w", err)
	}
	if err := os.Rename(tmpDstPath, dst); err != nil {
		_ = os.Remove(tmpDstPath)
		return fmt.Errorf("replace pointer file %s: %w", dst, err)
	}
	return nil
}

func partFilePath(resumeDir, oid string) string {
	return filepath.Join(resumeDir, oid+".part")
}

func multipartStatePath(partPath string) string {
	return partPath + ".state.json"
}

func resumeOffset(partPath string, wantSize int64) int64 {
	info, err := os.Stat(partPath)
	if err != nil {
		return 0
	}
	size := info.Size()
	if size < 0 {
		return 0
	}
	if wantSize > 0 && size > wantSize {
		return 0
	}
	return size
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func isHashMismatchError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "hash mismatch")
}

func loadOrInitMultipartState(path string, size, chunkSize int64, chunkCount int) (*multipartState, error) {
	st, err := loadMultipartState(path)
	if err == nil {
		if st.Size == size && st.ChunkSize == chunkSize && len(st.Done) == chunkCount {
			return st, nil
		}
	}
	st = &multipartState{
		Size:      size,
		ChunkSize: chunkSize,
		Done:      make([]bool, chunkCount),
	}
	if err := saveMultipartState(path, st); err != nil {
		return nil, err
	}
	return st, nil
}

func loadMultipartState(path string) (*multipartState, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var st multipartState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func saveMultipartState(path string, st *multipartState) error {
	b, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("marshal multipart state: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("write multipart state temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace multipart state file: %w", err)
	}
	return nil
}

func multipartDoneBytes(partPath string, wantSize int64) (int64, bool) {
	statePath := multipartStatePath(partPath)
	st, err := loadMultipartState(statePath)
	if err != nil {
		return 0, false
	}
	if st.Size != wantSize || st.ChunkSize <= 0 {
		return 0, false
	}
	chunks := buildChunks(wantSize, st.ChunkSize)
	if len(chunks) == 0 || len(st.Done) != len(chunks) {
		return 0, false
	}
	var doneBytes int64
	for i := 0; i < len(chunks); i++ {
		if st.Done[i] {
			doneBytes += chunks[i].end - chunks[i].start + 1
		}
	}
	if doneBytes <= 0 {
		return 0, true
	}
	if doneBytes > wantSize {
		doneBytes = wantSize
	}
	return doneBytes, true
}

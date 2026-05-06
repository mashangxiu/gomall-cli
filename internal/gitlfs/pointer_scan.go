package gitlfs

import (
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func collectPointers(root string) ([]pointerFile, error) {
	var out []pointerFile
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		// LFS pointer files are tiny text files.
		if info.Size() <= 0 || info.Size() > 8*1024 {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		oid, size, ok := parsePointer(raw)
		if !ok {
			return nil
		}
		out = append(out, pointerFile{
			Path: path,
			OID:  oid,
			Size: size,
			Mode: info.Mode(),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan lfs pointers: %w", err)
	}
	return out, nil
}

func parsePointer(raw []byte) (string, int64, bool) {
	content := strings.ReplaceAll(string(raw), "\r\n", "\n")
	lines := strings.Split(content, "\n")
	if len(lines) < 3 {
		return "", 0, false
	}
	if strings.TrimSpace(lines[0]) != "version https://git-lfs.github.com/spec/v1" {
		return "", 0, false
	}

	oidLine := strings.TrimSpace(lines[1])
	sizeLine := strings.TrimSpace(lines[2])
	if !strings.HasPrefix(oidLine, "oid sha256:") || !strings.HasPrefix(sizeLine, "size ") {
		return "", 0, false
	}
	oid := strings.TrimSpace(strings.TrimPrefix(oidLine, "oid sha256:"))
	if len(oid) != 64 {
		return "", 0, false
	}
	if _, err := hex.DecodeString(oid); err != nil {
		return "", 0, false
	}
	sizeText := strings.TrimSpace(strings.TrimPrefix(sizeLine, "size "))
	size, err := strconv.ParseInt(sizeText, 10, 64)
	if err != nil || size < 0 {
		return "", 0, false
	}
	return strings.ToLower(oid), size, true
}

func groupPointersByOID(pointers []pointerFile) map[string][]pointerFile {
	out := make(map[string][]pointerFile, len(pointers))
	for _, pf := range pointers {
		out[pf.OID] = append(out[pf.OID], pf)
	}
	return out
}

func buildTaskLabel(repoDir string, files []pointerFile) string {
	if len(files) == 0 {
		return "(unknown file)"
	}
	first := files[0].Path
	if rel, err := filepath.Rel(repoDir, first); err == nil && rel != "" && rel != "." && !strings.HasPrefix(rel, "..") {
		first = rel
	} else {
		first = filepath.Base(first)
	}
	if len(files) == 1 {
		return first
	}
	return fmt.Sprintf("%s (+%d 个同OID文件)", first, len(files)-1)
}

package gitlfs

import (
	"io"
	"io/fs"
	"time"
)

const (
	lfsContentType  = "application/vnd.git-lfs+json"
	batchOpDownload = "download"
	lfsMaxRetries   = 4
	lfsWorkerCount  = 2
	lfsBackoffBase  = 500 * time.Millisecond
	lfsBackoffMax   = 5 * time.Second
	lfsResumeDir    = ".gomall-cli-lfs"
	lfsChunkWorkers = 4
	lfsChunkSize    = 16 * 1024 * 1024  // 16MB
	lfsChunkMinSize = 256 * 1024 * 1024 // 256MB+
)

type HydrateOptions struct {
	RepoDir             string
	RepoURL             string
	Token               string
	UserAgent           string
	Insecure            bool
	HTTPTimeout         time.Duration
	IdleTimeout         time.Duration
	ChunkSize           int64
	DownloadURLOverride string
	IncludePaths        []string
	ProgressOut         io.Writer
	DebugBatch          bool
	DebugOut            io.Writer
}

type pointerFile struct {
	Path string
	OID  string
	Size int64
	Mode fs.FileMode
}

type batchRequest struct {
	Operation string               `json:"operation"`
	Transfers []string             `json:"transfers,omitempty"`
	Objects   []batchRequestObject `json:"objects"`
}

type batchRequestObject struct {
	OID  string `json:"oid"`
	Size int64  `json:"size"`
}

type batchResponse struct {
	Transfer string                `json:"transfer"`
	Objects  []batchResponseObject `json:"objects"`
}

type batchResponseObject struct {
	OID     string                    `json:"oid"`
	Size    int64                     `json:"size"`
	Actions map[string]batchAction    `json:"actions"`
	Error   *batchResponseObjectError `json:"error"`
}

type batchResponseObjectError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type batchAction struct {
	Href   string            `json:"href"`
	Header map[string]string `json:"header"`
}

type lfsDownloadTask struct {
	oid    string
	size   int64
	action batchAction
	label  string
	part   string
}

type rangeChunk struct {
	idx   int
	start int64
	end   int64
}

type multipartState struct {
	Size      int64  `json:"size"`
	ChunkSize int64  `json:"chunkSize"`
	Done      []bool `json:"done"`
}

type progressReader struct {
	r      io.Reader
	onRead func(int64)
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.r.Read(p)
	if n > 0 && pr.onRead != nil {
		pr.onRead(int64(n))
	}
	return n, err
}

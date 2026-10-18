package service

import (
	"context"
	"errors"
	"io"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

// StoredArtifactRef describes a persisted object and stays server-side; public
// task responses use authenticated artifact content URLs.
type StoredArtifactRef struct {
	Backend   string
	Bucket    string
	ObjectKey string
	MimeType  string
	Size      int64
}

// TaskArtifactStore is the persistence boundary for generated artifact bytes.
// types.TaskArtifact is re-exported by relay/channel as channel.TaskArtifact.
type TaskArtifactStore interface {
	Enabled() bool
	Resolve(task *model.Task, artifactKey string) (*StoredArtifactRef, error)
	Persist(ctx context.Context, task *model.Task, artifact types.TaskArtifact, content io.Reader) (*StoredArtifactRef, error)
	Serve(c *gin.Context, task *model.Task, ref *StoredArtifactRef) error
}

var ErrTaskArtifactStoreDisabled = errors.New("task artifact store is disabled")

var taskArtifactStore TaskArtifactStore = &seedanceArtifactStore{}

func init() {
	_ = system_setting.LoadTaskArtifactStoreConfig()
}

// GetTaskArtifactStore returns the process-wide artifact storage backend. This
// backend resolves persisted Seedance generated videos and enhanced final objects.
func GetTaskArtifactStore() TaskArtifactStore {
	return taskArtifactStore
}

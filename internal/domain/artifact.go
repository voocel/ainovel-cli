package domain

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// Artifact 是已发布工件的元数据（D47）：内容按 Digest 寻址、发布后不可变；
// ID 为 `<operation_id>/<key>`，归属产出它的执行尝试；Basis 记录生成时依据的
// 来源（D48）。进入权威只能经 Attachment 引用。
type Artifact struct {
	ID          string        `json:"id"`
	ProjectID   string        `json:"project_id"`
	Digest      string        `json:"digest"`
	MediaType   string        `json:"media_type"`
	Size        int64         `json:"size"`
	Basis       EvidenceBasis `json:"basis"`
	OperationID string        `json:"operation_id"`
	Attempt     int           `json:"attempt"`
	CreatedAt   time.Time     `json:"created_at"`
}

func ArtifactID(operationID, key string) string {
	return operationID + "/" + key
}

func (a Artifact) Validate() error {
	prefix := a.OperationID + "/"
	if strings.TrimSpace(a.OperationID) == "" || !strings.HasPrefix(a.ID, prefix) || strings.TrimSpace(strings.TrimPrefix(a.ID, prefix)) == "" {
		return fmt.Errorf("artifact id must be <operation_id>/<key>: %w", ErrInvalid)
	}
	if strings.TrimSpace(a.ProjectID) == "" || strings.TrimSpace(a.Digest) == "" || strings.TrimSpace(a.MediaType) == "" {
		return fmt.Errorf("artifact project, digest and media type are required: %w", ErrInvalid)
	}
	if err := ValidateArtifactDigest(a.Digest); err != nil {
		return err
	}
	if a.Size < 0 || a.Attempt <= 0 || a.CreatedAt.IsZero() {
		return fmt.Errorf("artifact size, attempt and creation time are invalid: %w", ErrInvalid)
	}
	return a.Basis.Validate()
}

func (a Artifact) Ref() ArtifactRef {
	return ArtifactRef{ID: a.ID, Digest: a.Digest}
}

// ArtifactRef 引用一个已发布的工件：ID 定位元数据行，Digest 钉住内容身份。
type ArtifactRef struct {
	ID     string `json:"id"`
	Digest string `json:"digest"`
}

func (r ArtifactRef) Validate() error {
	if strings.TrimSpace(r.ID) == "" || strings.TrimSpace(r.Digest) == "" {
		return fmt.Errorf("artifact reference requires id and digest: %w", ErrInvalid)
	}
	return ValidateArtifactDigest(r.Digest)
}

// ValidateArtifactDigest 同时限定内容身份和对象路径；只接受规范的小写 SHA256。
func ValidateArtifactDigest(digest string) error {
	if len(digest) != 64 || strings.ToLower(digest) != digest {
		return fmt.Errorf("artifact digest must be a lowercase SHA256 hex digest: %w", ErrInvalid)
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return fmt.Errorf("artifact digest must be a lowercase SHA256 hex digest: %w", ErrInvalid)
	}
	return nil
}

// Attachment 是工件进入权威的唯一方式（D47）：把一个已发布工件以 Role 挂到
// Target 文档上；Target 变化时附件随结构影响进入"可能受影响"。
type Attachment struct {
	ID        string        `json:"id"`
	Target    DocumentRef   `json:"target"`
	Role      string        `json:"role"`
	Artifact  ArtifactRef   `json:"artifact"`
	DependsOn []DocumentRef `json:"depends_on,omitempty"`
}

func (v Attachment) Validate() error {
	if strings.TrimSpace(v.ID) == "" || strings.TrimSpace(v.Role) == "" {
		return fmt.Errorf("attachment id and role are required: %w", ErrInvalid)
	}
	if err := v.Target.Validate(); err != nil {
		return err
	}
	if v.Target.Kind == DocumentAttachment {
		return fmt.Errorf("attachment cannot target another attachment: %w", ErrInvalid)
	}
	if err := v.Artifact.Validate(); err != nil {
		return err
	}
	return validateDocumentRefs(v.DependsOn)
}

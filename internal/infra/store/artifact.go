package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain/model"
)

// 工件对象存储（D47）：每个数据库有独立的 <db>.artifacts/，内容按 sha256 寻址。
// Publish 在 SQLite 写事务内发布并登记 pin；元数据接替 pin 后对象才可正式引用。
// 元数据行记录归属；进入权威只能经 attachment 文档引用。

type ArtifactWriter struct {
	store       *Store
	root        string
	operationID string
	attempt     int
	file        *os.File
	hash        hash.Hash
	size        int64
}

func (s *Store) NewArtifactWriter(operationID string, attempt int) (*ArtifactWriter, error) {
	if strings.TrimSpace(operationID) == "" || attempt <= 0 {
		return nil, fmt.Errorf("artifact writer requires operation and positive attempt: %w", model.ErrInvalid)
	}
	tx, err := s.beginArtifactWrite(context.Background())
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	dir := artifactStagingDir(s.artifactRoot, operationID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create artifact staging: %w", err)
	}
	file, err := os.CreateTemp(dir, fmt.Sprintf("%d-*.tmp", attempt))
	if err != nil {
		return nil, fmt.Errorf("create artifact staging file: %w", err)
	}
	if err := tx.Commit(); err != nil {
		file.Close()
		os.Remove(file.Name())
		return nil, fmt.Errorf("commit artifact staging creation: %w", err)
	}
	return &ArtifactWriter{store: s, root: s.artifactRoot, operationID: operationID, attempt: attempt, file: file, hash: sha256.New()}, nil
}

func (w *ArtifactWriter) Write(p []byte) (int, error) {
	if w.file == nil {
		return 0, fmt.Errorf("artifact writer is closed: %w", model.ErrInvalid)
	}
	n, err := w.file.Write(p)
	w.hash.Write(p[:n])
	w.size += int64(n)
	return n, err
}

// Publish 原子发布暂存内容：fsync 文件 → 改名到内容地址 → fsync 目录；同摘要
// 对象已存在时丢弃暂存副本，同样登记 pin，避免重用旧孤儿对象时被 GC 删除。
func (w *ArtifactWriter) Publish() (string, int64, error) {
	if w.file == nil {
		return "", 0, fmt.Errorf("artifact writer is closed: %w", model.ErrInvalid)
	}
	staging := w.file.Name()
	if err := w.file.Sync(); err != nil {
		w.Abort()
		return "", 0, fmt.Errorf("sync artifact: %w", err)
	}
	if err := w.file.Close(); err != nil {
		w.file = nil
		os.Remove(staging)
		return "", 0, fmt.Errorf("close artifact: %w", err)
	}
	w.file = nil
	digest := hex.EncodeToString(w.hash.Sum(nil))
	tx, err := w.store.beginArtifactWrite(context.Background())
	if err != nil {
		os.Remove(staging)
		return "", 0, err
	}
	defer tx.Rollback()
	target := objectPath(w.root, digest)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		os.Remove(staging)
		return "", 0, fmt.Errorf("create artifact object directory: %w", err)
	}
	if _, err := os.Stat(target); err == nil {
		if err := os.Remove(staging); err != nil {
			return "", 0, fmt.Errorf("remove duplicate staging artifact: %w", err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		os.Remove(staging)
		return "", 0, fmt.Errorf("stat artifact object: %w", err)
	} else {
		if err := os.Rename(staging, target); err != nil {
			os.Remove(staging)
			return "", 0, fmt.Errorf("publish artifact: %w", err)
		}
	}
	// 新建分片目录的父目录也需同步，否则掉电后可能仅留下引用元数据。
	for _, dir := range []string{filepath.Dir(target), filepath.Join(w.root, "objects"), w.root, filepath.Dir(w.root)} {
		if err := syncDir(dir); err != nil {
			return "", 0, err
		}
	}
	if _, err := tx.Exec(`INSERT INTO artifact_publications (operation_id, attempt, content_digest, created_at_unix_ms)
		VALUES (?, ?, ?, ?) ON CONFLICT (operation_id, attempt, content_digest)
		DO UPDATE SET created_at_unix_ms = excluded.created_at_unix_ms`, w.operationID, w.attempt, digest, time.Now().UnixMilli()); err != nil {
		return "", 0, fmt.Errorf("pin artifact publication: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", 0, fmt.Errorf("commit artifact publication: %w", err)
	}
	return digest, w.size, nil
}

func (w *ArtifactWriter) Abort() error {
	if w.file == nil {
		return nil
	}
	name := w.file.Name()
	w.file.Close()
	w.file = nil
	return os.Remove(name)
}

func objectPath(root, digest string) string {
	return filepath.Join(root, "objects", digest[:2], digest)
}

// 任务 ID 是不透明标识，不把斜杠、长度等文件系统限制加到领域 ID 上。
func artifactStagingDir(root, operationID string) string {
	return filepath.Join(root, "staging", model.Digest([]byte(operationID)))
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open artifact directory: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync artifact directory: %w", err)
	}
	return nil
}

// ReadArtifact 读取已发布对象的内容。
func (s *Store) ReadArtifact(digest string) ([]byte, error) {
	if err := model.ValidateArtifactDigest(digest); err != nil {
		return nil, err
	}
	content, err := os.ReadFile(objectPath(s.artifactRoot, digest))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, model.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read artifact object: %w", err)
	}
	return content, nil
}

// SaveExecutionArtifacts 在执行归属围栏内落盘工件元数据（D42/D47）：对象必须已
// 发布且大小一致；同 ID 的内容与基线不可替换，等价重执行只更新执行归属。
func (s *Store) SaveExecutionArtifacts(ctx context.Context, artifacts []model.Artifact, operationID string, attempt int) error {
	tx, err := s.beginArtifactWrite(ctx)
	if err != nil {
		return fmt.Errorf("begin artifact save: %w", err)
	}
	defer tx.Rollback()
	if err := assertActiveAttempt(ctx, tx, operationID, attempt); err != nil {
		return err
	}
	var projectID string
	if err := tx.QueryRowContext(ctx, `SELECT target_id FROM operations WHERE id = ?`, operationID).Scan(&projectID); err != nil {
		return fmt.Errorf("read artifact operation: %w", err)
	}
	seen := make(map[string]struct{}, len(artifacts))
	for _, artifact := range artifacts {
		if err := artifact.Validate(); err != nil {
			return err
		}
		if artifact.OperationID != operationID || artifact.Attempt != attempt || artifact.ProjectID != projectID {
			return fmt.Errorf("artifact %q does not belong to operation %q attempt %d: %w", artifact.ID, operationID, attempt, model.ErrInvalid)
		}
		if _, ok := seen[artifact.ID]; ok {
			return fmt.Errorf("duplicate artifact %q: %w", artifact.ID, model.ErrInvalid)
		}
		seen[artifact.ID] = struct{}{}
		info, err := os.Stat(objectPath(s.artifactRoot, artifact.Digest))
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("artifact %q object %s is not published: %w", artifact.ID, artifact.Digest, model.ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("stat artifact object: %w", err)
		}
		if info.Size() != artifact.Size {
			return fmt.Errorf("artifact %q size %d does not match object %d: %w", artifact.ID, artifact.Size, info.Size(), model.ErrInvalid)
		}
		basis, err := json.Marshal(artifact.Basis.Normalize())
		if err != nil {
			return fmt.Errorf("encode artifact basis: %w", err)
		}
		result, err := tx.ExecContext(ctx, `
			INSERT INTO artifacts (id, project_id, content_digest, media_type, size, basis, operation_id, attempt, created_at_unix_ms)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (id) DO UPDATE SET
				attempt = excluded.attempt, created_at_unix_ms = excluded.created_at_unix_ms
				WHERE artifacts.content_digest = excluded.content_digest AND artifacts.media_type = excluded.media_type
					AND artifacts.size = excluded.size AND artifacts.basis = excluded.basis
					AND artifacts.operation_id = excluded.operation_id AND artifacts.project_id = excluded.project_id`,
			artifact.ID, artifact.ProjectID, artifact.Digest, artifact.MediaType, artifact.Size, basis,
			artifact.OperationID, artifact.Attempt, artifact.CreatedAt.UnixMilli())
		if err != nil {
			return fmt.Errorf("save artifact %q: %w", artifact.ID, err)
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("inspect artifact save: %w", err)
		}
		if rows != 1 {
			return fmt.Errorf("artifact %q already identifies different ownership, content or evidence: %w", artifact.ID, model.ErrIdempotencyConflict)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM artifact_publications WHERE operation_id = ? AND attempt = ? AND content_digest = ?`, operationID, attempt, artifact.Digest); err != nil {
			return fmt.Errorf("release artifact publication: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit artifact save: %w", err)
	}
	return nil
}

// beginArtifactWrite 先取得 SQLite 写锁，再检查文件和引用。Publish、元数据提交和
// GC 使用同一顺序，因此多个 Store 或进程间也不存在查完引用后被并发删除的窗口。
func (s *Store) beginArtifactWrite(ctx context.Context) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin artifact transaction: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE artifact_publications SET attempt = attempt WHERE 0`); err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("lock artifact publication and collection: %w", err)
	}
	return tx, nil
}

const artifactColumns = `id, project_id, content_digest, media_type, size, basis, operation_id, attempt, created_at_unix_ms`

func (s *Store) GetArtifact(ctx context.Context, id string) (model.Artifact, error) {
	artifact, err := scanArtifact(s.db.QueryRowContext(ctx, `SELECT `+artifactColumns+` FROM artifacts WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Artifact{}, model.ErrNotFound
	}
	if err != nil {
		return model.Artifact{}, fmt.Errorf("read artifact: %w", err)
	}
	return artifact, nil
}

func (s *Store) ListArtifacts(ctx context.Context, projectID string) ([]model.Artifact, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+artifactColumns+` FROM artifacts WHERE project_id = ? ORDER BY id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list artifacts: %w", err)
	}
	defer rows.Close()
	artifacts := make([]model.Artifact, 0)
	for rows.Next() {
		artifact, err := scanArtifact(rows)
		if err != nil {
			return nil, fmt.Errorf("scan artifact: %w", err)
		}
		artifacts = append(artifacts, artifact)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate artifacts: %w", err)
	}
	return artifacts, nil
}

func scanArtifact(scanner rowScanner) (model.Artifact, error) {
	var artifact model.Artifact
	var basis []byte
	var createdAt int64
	if err := scanner.Scan(&artifact.ID, &artifact.ProjectID, &artifact.Digest, &artifact.MediaType,
		&artifact.Size, &basis, &artifact.OperationID, &artifact.Attempt, &createdAt); err != nil {
		return model.Artifact{}, err
	}
	if err := json.Unmarshal(basis, &artifact.Basis); err != nil {
		return model.Artifact{}, fmt.Errorf("decode artifact basis: %w", err)
	}
	artifact.CreatedAt = time.UnixMilli(createdAt).UTC()
	return artifact, nil
}

// CollectArtifactGarbage 删除无元数据或发布 pin 引用且超过宽限期的对象。
// pin 仅在原尝试已不再 running 且过宽限期后释放；当前尝试的暂存文件也受保护。
// 只显式调用，不隐式触发。
func (s *Store) CollectArtifactGarbage(ctx context.Context, now time.Time, grace time.Duration) (int, error) {
	if now.IsZero() || grace < 0 {
		return 0, fmt.Errorf("garbage collection requires time and non-negative grace: %w", model.ErrInvalid)
	}
	cutoff := now.Add(-grace)
	tx, err := s.beginArtifactWrite(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM artifact_publications
		WHERE created_at_unix_ms < ? AND NOT EXISTS (
			SELECT 1 FROM operations WHERE operations.id = artifact_publications.operation_id
			AND operations.attempt = artifact_publications.attempt AND operations.state = ?
		)`, cutoff.UnixMilli(), model.OperationRunning); err != nil {
		return 0, fmt.Errorf("release expired artifact publications: %w", err)
	}
	removed := 0
	objects := filepath.Join(s.artifactRoot, "objects")
	err = filepath.WalkDir(objects, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.ModTime().Before(cutoff) {
			return nil
		}
		var referenced bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM artifacts WHERE content_digest = ?)
			OR EXISTS (SELECT 1 FROM artifact_publications WHERE content_digest = ?)`, entry.Name(), entry.Name()).Scan(&referenced); err != nil {
			return fmt.Errorf("check artifact reference: %w", err)
		}
		if referenced {
			return nil
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove artifact object: %w", err)
		}
		removed++
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return removed, err
	}
	staging := filepath.Join(s.artifactRoot, "staging")
	operations, err := os.ReadDir(staging)
	if errors.Is(err, fs.ErrNotExist) {
		return removed, tx.Commit()
	}
	if err != nil {
		return removed, fmt.Errorf("read artifact staging: %w", err)
	}
	active := make(map[string]int)
	rows, err := tx.QueryContext(ctx, `SELECT id, attempt FROM operations WHERE state = ?`, model.OperationRunning)
	if err != nil {
		return removed, err
	}
	for rows.Next() {
		var id string
		var attempt int
		if err := rows.Scan(&id, &attempt); err != nil {
			rows.Close()
			return removed, err
		}
		active[model.Digest([]byte(id))] = attempt
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return removed, err
	}
	rows.Close()
	for _, operation := range operations {
		if !operation.IsDir() {
			continue
		}
		dir := filepath.Join(staging, operation.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			return removed, fmt.Errorf("read staging directory: %w", err)
		}
		for _, file := range files {
			attemptText, _, _ := strings.Cut(file.Name(), "-")
			attempt, err := strconv.Atoi(attemptText)
			if err != nil {
				return removed, fmt.Errorf("invalid artifact staging attempt %q: %w", file.Name(), err)
			}
			if active[operation.Name()] == attempt {
				continue
			}
			info, err := file.Info()
			if err != nil {
				return removed, err
			}
			if !info.ModTime().Before(cutoff) {
				continue
			}
			if err := os.Remove(filepath.Join(dir, file.Name())); err != nil {
				return removed, fmt.Errorf("remove staging file: %w", err)
			}
			removed++
		}
		os.Remove(dir) // 非空时失败即保留
	}
	if err := tx.Commit(); err != nil {
		return removed, fmt.Errorf("commit artifact collection: %w", err)
	}
	return removed, nil
}

package headless

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/app/resource"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain/model"
	"github.com/voocel/ainovel-cli/internal/infra/store"
)

func TestPackURLInstallChecksSHA256BeforeCommitting(t *testing.T) {
	const payload = `{"format":"ainovel.novelpack.v1","manifest":{"id":"style","version":"1","name":"Style"}}`
	sum := sha256.Sum256([]byte(payload))
	checksum := hex.EncodeToString(sum[:])
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, payload)
	}))
	defer server.Close()
	for _, tc := range []struct {
		name, checksum string
		wantError      bool
	}{
		{name: "existing command"},
		{name: "verified archive", checksum: checksum},
		{name: "mismatch", checksum: strings.Repeat("0", 64), wantError: true},
		{name: "malformed checksum", checksum: "invalid", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s, err := store.Open(ctx, filepath.Join(t.TempDir(), "ainovel.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			api := bootstrap.New(s, bootstrap.Options{})
			args := []string{"pack", "install", "--url", server.URL, "--change", "install", "--user", "user", "--reason", "安装创作包"}
			if tc.checksum != "" {
				args = append(args, "--sha256", tc.checksum)
			}
			var stdout, stderr bytes.Buffer
			err = Run(ctx, api, args, &stdout, &stderr)
			if tc.wantError {
				if !errors.Is(err, model.ErrInvalid) || stdout.Len() != 0 {
					t.Fatalf("invalid checksum: output=%q err=%v", stdout.String(), err)
				}
				if _, err := s.CurrentRevision(ctx, model.AuthorityTarget{Kind: model.AuthorityPack, ID: "style"}); !errors.Is(err, model.ErrNotFound) {
					t.Fatalf("failed verification must not create pack authority: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("install: %v; stderr=%q", err, stderr.String())
			}
			var installed resource.InstalledPack
			if err := json.Unmarshal(stdout.Bytes(), &installed); err != nil {
				t.Fatal(err)
			}
			if installed.Manifest.ID != "style" || installed.Revision != 1 || installed.Digest == "" {
				t.Fatalf("installed=%#v", installed)
			}
		})
	}
}

func TestPackSHA256RequiresURL(t *testing.T) {
	for _, source := range []string{"--dir", "--file"} {
		args := []string{"pack", "install", source, "style", "--sha256", strings.Repeat("0", 64), "--change", "install", "--user", "user", "--reason", "安装"}
		if err := Run(context.Background(), nil, args, &bytes.Buffer{}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "--url") {
			t.Errorf("checksum with %s: %v", source, err)
		}
	}
}

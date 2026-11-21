package lsget

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
)

const readmePreviewLimit = 256 * 1024

func readDocPreview(ctx context.Context, root *os.Root, name string) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	file, _, err := openVisibleFile(root, name)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = file.Close() }()

	body, err := io.ReadAll(io.LimitReader(contextReader{ctx, file}, readmePreviewLimit+1))
	if err != nil {
		return "", false, err
	}
	if len(body) <= readmePreviewLimit {
		return string(body), false, nil
	}
	return string(body[:readmePreviewLimit]), true, nil
}

// readDocFile selects the most relevant visible document and previews its contents.
func readDocFile(ctx context.Context, root *os.Root, dir string) (string, string, bool) {
	ents, err := readVisibleDir(ctx, newWorkBudget(), root, dir)
	if err != nil && !errors.Is(err, errWorkLimit) {
		return "", "", false
	}

	// Priority order for documentation files
	docFiles := []struct {
		pattern  string
		fileType string
	}{
		{"README.md", "markdown"},
		{"README.txt", "text"},
		{"README.rst", "rst"},
		{"README.nfo", "nfo"},
	}

	// First, try exact matches in priority order
	for _, docFile := range docFiles {
		if errors.Is(err, errWorkLimit) {
			// A canonical README may be beyond the bounded directory sample.
			body, truncated, previewErr := readDocPreview(ctx, root, path.Join(dir, docFile.pattern))
			if previewErr == nil {
				return body, docFile.fileType, truncated
			}
		}
		for _, e := range ents {
			if ctx.Err() != nil {
				return "", "", false
			}
			if strings.EqualFold(e.Name(), docFile.pattern) {
				body, truncated, err := readDocPreview(ctx, root, path.Join(dir, e.Name()))
				if err != nil {
					continue
				}
				return body, docFile.fileType, truncated
			}
		}
	}

	// Then try any file with supported extensions
	for _, e := range ents {
		if ctx.Err() != nil {
			return "", "", false
		}
		ext := path.Ext(strings.ToLower(e.Name()))
		for _, docFile := range docFiles {
			if ext == path.Ext(docFile.pattern) {
				body, truncated, err := readDocPreview(ctx, root, path.Join(dir, e.Name()))
				if err == nil {
					return body, docFile.fileType, truncated
				}
				break
			}
		}
	}

	return "", "", false
}

type configResp struct {
	CatMax          int64   `json:"catMax"`
	Readme          *string `json:"readme,omitempty"`
	DocType         string  `json:"docType,omitempty"`
	ReadmeTruncated bool    `json:"readmeTruncated,omitempty"`
	CWD             string  `json:"cwd,omitempty"`
}

func (s *server) handleConfig(w http.ResponseWriter, r *http.Request) {
	requestedCWD := requestCWD(r)
	initialPath := r.URL.Query().Get("path")
	if initialPath != "" && !s.acceptSessionOrigin(r) {
		http.Error(w, "forbidden request origin", http.StatusForbidden)
		return
	}
	release := s.reserveWork(w, r)
	if release == nil {
		return
	}
	defer release()
	var sess *session
	cwd := requestedCWD
	if cwd == "" {
		sess = s.acquireSession(w, r)
		if sess == nil {
			return
		}
		defer s.releaseSession(sess)
		cwd = sess.workingDirectory()
	}
	var readme, docType string
	var readmeTruncated bool
	if root, err := os.OpenRoot(s.rootAbs); err == nil {
		defer func() { _ = root.Close() }()
		if requestedCWD != "" {
			if _, err := resolveDirectory(root, "/", cwd); err != nil {
				http.NotFound(w, r)
				return
			}
		} else if initialPath != "" {
			if newV, err := sess.changeDirectory(root, cleanVirtual(initialPath)); err == nil {
				cwd = newV
			}
		}
		readme, docType, readmeTruncated = readDocFile(r.Context(), root, rootedName(cwd))
	} else if requestedCWD != "" {
		http.Error(w, "cannot open served directory", http.StatusServiceUnavailable)
		return
	}
	if r.Context().Err() != nil {
		return
	}

	_ = json.NewEncoder(w).Encode(configResp{
		CatMax: s.catMax, Readme: &readme, DocType: docType, ReadmeTruncated: readmeTruncated, CWD: cwd,
	})
}

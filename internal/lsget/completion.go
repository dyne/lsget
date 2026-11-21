package lsget

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"sort"
	"strings"
)

type completeReq struct {
	Path      string `json:"path"`
	DirsOnly  bool   `json:"dirsOnly"`
	FilesOnly bool   `json:"filesOnly"`
	TextOnly  bool   `json:"textOnly"`
	MaxSize   int64  `json:"maxSize"`
}

type completeItem struct {
	Name string `json:"name"`
	Dir  bool   `json:"dir"`
}

type completeResp struct {
	Items     []completeItem `json:"items"`
	Truncated bool           `json:"truncated,omitempty"`
}

func (s *server) handleComplete(w http.ResponseWriter, r *http.Request) {
	var req completeReq
	if !s.readCommandRequest(w, r, &req) {
		return
	}
	release := s.reserveWork(w, r)
	if release == nil {
		return
	}
	defer release()
	ctx := r.Context()

	arg := req.Path
	requestedCWD := requestCWD(r)
	if requestedCWD != "" && req.TextOnly && req.MaxSize == 0 {
		req.MaxSize = s.catMax
	}
	// Split into dir part + base (prefix)
	slash := strings.LastIndex(arg, "/")
	dirPart := ""
	basePart := arg
	if slash >= 0 {
		dirPart = arg[:slash+1] // keep trailing slash
		basePart = arg[slash+1:]
	}

	// Resolve base virtual directory
	var baseV string
	if strings.HasPrefix(arg, "/") {
		baseV = cleanVirtual(dirPart)
	} else if requestedCWD != "" {
		baseV = joinVirtual(requestedCWD, dirPart)
	} else {
		sess := s.acquireSession(w, r)
		if sess == nil {
			return
		}
		defer s.releaseSession(sess)
		baseV = joinVirtual(sess.workingDirectory(), dirPart)
	}
	root, err := os.OpenRoot(s.rootAbs)
	if err != nil {
		_ = json.NewEncoder(w).Encode(completeResp{Items: nil})
		return
	}

	defer func() { _ = root.Close() }()
	base := rootedName(baseV)
	budget := newWorkBudget()
	ents, err := readVisibleDir(ctx, budget, root, base)
	if ctx.Err() != nil {
		return
	}
	truncated := errors.Is(err, errWorkLimit)
	if err != nil && !truncated {
		_ = json.NewEncoder(w).Encode(completeResp{Items: nil})
		return
	}

	showHidden := strings.HasPrefix(basePart, ".")
	const maxItems = 200
	items := make([]completeItem, 0, 16)

	for _, e := range ents {
		if ctx.Err() != nil {
			return
		}
		name := e.Name()
		if !strings.HasPrefix(name, basePart) {
			continue
		}
		if !showHidden && strings.HasPrefix(name, ".") {
			continue
		}

		child, err := budget.childName(base, name)
		if err != nil {
			truncated = true
			break
		}
		info, err := visibleInfo(root, child)
		if err != nil {
			continue
		}
		isDir := info.IsDir()
		if req.DirsOnly && !isDir {
			continue
		}

		if !isDir && (req.TextOnly || req.MaxSize > 0) {
			category := getFileCategory(name)
			// Images remain eligible for inline cat regardless of size.
			if category != FileCategoryImage && (req.MaxSize > 0 && info.Size() > req.MaxSize ||
				req.TextOnly && category != FileCategoryText) {
				continue
			}
		}

		if len(items) == maxItems {
			truncated = true
			break
		}
		items = append(items, completeItem{Name: name, Dir: isDir})
	}

	// Sort: directories first, then files; alphabetical within each
	sort.Slice(items, func(i, j int) bool {
		if items[i].Dir != items[j].Dir {
			return items[i].Dir && !items[j].Dir
		}
		return items[i].Name < items[j].Name
	})

	_ = json.NewEncoder(w).Encode(completeResp{Items: items, Truncated: truncated})
}

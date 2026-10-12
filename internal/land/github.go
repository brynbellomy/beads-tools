package land

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/brynbellomy/go-utils/errors"

	"github.com/brynbellomy/beads-tools/internal/gitx"
)

// PR is the part of a pull request landing needs.
type PR struct {
	Number            int    `json:"number"`
	State             string `json:"state"`
	URL               string `json:"url"`
	BaseRefName       string `json:"baseRefName"`
	HeadRefName       string `json:"headRefName"`
	HeadRefOid        string `json:"headRefOid"`
	IsCrossRepository bool   `json:"isCrossRepository"`
}

// GitHub is the pull request host.
type GitHub interface {
	// View reads ref (a number or URL), the same reference Merge takes.
	View(ctx context.Context, ref string) (PR, error)
	Create(ctx context.Context, base, head, title, body string) (url string, err error)
	// Merge merges ref (a number or URL) only if its head is still headSHA.
	Merge(ctx context.Context, ref, headSHA, method string) error
}

// GH drives GitHub through the gh CLI from Dir.
type GH struct {
	Dir string
}

// View implements GitHub.
func (g GH) View(ctx context.Context, ref string) (PR, error) {
	out, err := gitx.Command(ctx, g.Dir, "gh", "pr", "view", ref,
		"--json", "number,state,url,baseRefName,headRefName,headRefOid,isCrossRepository")
	if err != nil {
		return PR{}, err
	}
	var pr PR
	if err := json.Unmarshal([]byte(out), &pr); err != nil {
		return PR{}, errors.With(err, "decoding gh pr view").Err()
	}
	return pr, nil
}

// Create implements GitHub.
func (g GH) Create(ctx context.Context, base, head, title, body string) (string, error) {
	out, err := gitx.Command(ctx, g.Dir, "gh", "pr", "create", "--base", base, "--head", head, "--title", title, "--body", body)
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	return strings.TrimSpace(lines[len(lines)-1]), nil
}

// Merge implements GitHub.
func (g GH) Merge(ctx context.Context, ref, headSHA, method string) error {
	switch method {
	case "merge", "squash", "rebase":
	default:
		return errors.WithNew("unknown merge method").Set("method", method).Err()
	}
	_, err := gitx.Command(ctx, g.Dir, "gh", "pr", "merge", ref, "--"+method, "--match-head-commit", headSHA)
	return err
}

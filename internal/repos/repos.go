// Package repos define la interfaz de forjas de código y su implementación para GitHub.
package repos

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// RepoInfo son los metadatos que usa la factory de un repo.
type RepoInfo struct {
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
}

// ReviewItem es un comentario de revisión de un PR.
type ReviewItem struct {
	Author string
	Body   string
	Path   string // fichero (solo en comentarios inline)
	At     time.Time
}

// PRInfo es el estado de un pull request.
type PRInfo struct {
	Merged   bool
	HeadSHA  string
	MergeSHA string
	State    string // mergeable_state de GitHub
	PRState  string // estado del PR: open o closed
}

// CheckSummary es el estado de los checks de un PR tal como lo calcula GitHub.
// MergeState: clean (todo OK), unstable (algún check falla), blocked, behind, dirty, draft o unknown (calculando).
type CheckSummary struct {
	MergeState  string
	ActionsRuns int
}

// CIResult es el resultado agregado de CI de un commit.
type CIResult struct {
	State    string   // none | pending | success | failure
	Failures []string // "contexto: URL" de los checks que fallan
}

// Host es lo que la factory necesita de cualquier servidor de repos.
type Host interface {
	ParseRepo(repoURL string) (owner, name string, err error)
	GetRepo(owner, name string) (RepoInfo, error)
	// CreatePR abre un pull request de head sobre base y devuelve su URL web.
	CreatePR(owner, name, head, base, title, body string) (string, error)
	// PRInfo devuelve el estado de un pull request.
	PRInfo(owner, name string, number int) (PRInfo, error)
	// PRCheck devuelve el estado de los checks de un PR (según GitHub) y las ejecuciones de Actions.
	PRCheck(owner, name string, number int) (CheckSummary, error)
	// LatestSemverTag devuelve el tag vX.Y.Z más alto del repo ("" si no hay ninguno).
	LatestSemverTag(owner, name string) (string, error)
	// CreateTag crea un tag ligero apuntando a sha.
	CreateTag(owner, name, tag, sha string) error
	// ReviewsSince devuelve los comentarios de code review (inline, reviews con texto y comentarios
	// generales) creados después de `since`.
	ReviewsSince(owner, name string, number int, since time.Time) ([]ReviewItem, error)
	// FindPR devuelve la URL del PR abierto de `head` hacia `base`, si existe.
	FindPR(owner, name, head, base string) (string, bool, error)
	// Token devuelve el token para autenticar operaciones git (push).
	Token() string
	Ping() error
}

// GitHub implementa Host contra la API REST de GitHub.
type GitHub struct {
	api    string
	token  string
	client *http.Client
}

func NewGitHub(token string) *GitHub {
	return &GitHub{
		api:    "https://api.github.com",
		token:  token,
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

func (g *GitHub) get(path string, out any) error {
	req, err := http.NewRequest(http.MethodGet, g.api+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := g.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d", path, resp.StatusCode)
	}
	return json.Unmarshal(body, out)
}

func (g *GitHub) ParseRepo(repoURL string) (string, string, error) {
	u, err := url.Parse(repoURL)
	if err != nil {
		return "", "", err
	}
	if u.Host != "github.com" && u.Host != "www.github.com" {
		return "", "", fmt.Errorf("URL de GitHub no válida: %s", repoURL)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("URL de GitHub no válida: %s", repoURL)
	}
	return parts[0], strings.TrimSuffix(parts[1], ".git"), nil
}

func (g *GitHub) GetRepo(owner, name string) (RepoInfo, error) {
	var info RepoInfo
	err := g.get(fmt.Sprintf("/repos/%s/%s", owner, name), &info)
	return info, err
}

func (g *GitHub) Token() string { return g.token }

func (g *GitHub) CreatePR(owner, name, head, base, title, body string) (string, error) {
	payload, err := json.Marshal(map[string]string{"title": title, "head": head, "base": base, "body": body})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/repos/%s/%s/pulls", g.api, owner, name), strings.NewReader(string(payload)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("crear PR: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var pr struct {
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal(data, &pr); err != nil {
		return "", err
	}
	return pr.HTMLURL, nil
}

func (g *GitHub) Ping() error {
	var user map[string]any
	return g.get("/user", &user)
}

func (g *GitHub) PRInfo(owner, name string, number int) (PRInfo, error) {
	var pr struct {
		Merged         bool   `json:"merged"`
		MergeCommitSHA string `json:"merge_commit_sha"`
		MergeableState string `json:"mergeable_state"`
		PRState        string `json:"state"`
		Head           struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	err := g.get(fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, name, number), &pr)
	return PRInfo{Merged: pr.Merged, HeadSHA: pr.Head.SHA, MergeSHA: pr.MergeCommitSHA, State: pr.MergeableState, PRState: pr.PRState}, err
}

func (g *GitHub) PRCheck(owner, name string, number int) (CheckSummary, error) {
	info, err := g.PRInfo(owner, name, number)
	if err != nil {
		return CheckSummary{}, err
	}
	sum := CheckSummary{MergeState: info.State}
	// Actions: si el repo no lo usa, total_count es 0; si el token no puede leerlo, se ignora.
	var runs struct {
		TotalCount int `json:"total_count"`
	}
	if err := g.get(fmt.Sprintf("/repos/%s/%s/actions/runs?head_sha=%s&per_page=1", owner, name, info.HeadSHA), &runs); err == nil {
		sum.ActionsRuns = runs.TotalCount
	}
	return sum, nil
}

func (g *GitHub) LatestSemverTag(owner, name string) (string, error) {
	var tags []struct {
		Name string `json:"name"`
	}
	if err := g.get(fmt.Sprintf("/repos/%s/%s/tags?per_page=100", owner, name), &tags); err != nil {
		return "", err
	}
	best := ""
	bestParts := [3]int{-1, -1, -1}
	for _, t := range tags {
		var a, b, c int
		if _, err := fmt.Sscanf(t.Name, "v%d.%d.%d", &a, &b, &c); err != nil {
			continue
		}
		if a > bestParts[0] || (a == bestParts[0] && (b > bestParts[1] || (b == bestParts[1] && c > bestParts[2]))) {
			best, bestParts = t.Name, [3]int{a, b, c}
		}
	}
	return best, nil
}

func (g *GitHub) CreateTag(owner, name, tag, sha string) error {
	payload, err := json.Marshal(map[string]string{"ref": "refs/tags/" + tag, "sha": sha})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/repos/%s/%s/git/refs", g.api, owner, name), strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		data, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("crear tag %s: HTTP %d: %s", tag, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return nil
}

func (g *GitHub) ReviewsSince(owner, name string, number int, since time.Time) ([]ReviewItem, error) {
	base := fmt.Sprintf("/repos/%s/%s", owner, name)
	var items []ReviewItem

	var inline []struct {
		User      struct{ Login string } `json:"user"`
		Body      string                 `json:"body"`
		Path      string                 `json:"path"`
		CreatedAt time.Time              `json:"created_at"`
	}
	if err := g.get(fmt.Sprintf("%s/pulls/%d/comments?per_page=100", base, number), &inline); err != nil {
		return nil, err
	}
	for _, c := range inline {
		if c.CreatedAt.After(since) && !isBotComment(c.Body) {
			items = append(items, ReviewItem{Author: c.User.Login, Body: c.Body, Path: c.Path, At: c.CreatedAt})
		}
	}

	var reviews []struct {
		User        struct{ Login string } `json:"user"`
		Body        string                 `json:"body"`
		State       string                 `json:"state"`
		SubmittedAt time.Time              `json:"submitted_at"`
	}
	if err := g.get(fmt.Sprintf("%s/pulls/%d/reviews?per_page=100", base, number), &reviews); err != nil {
		return nil, err
	}
	for _, r := range reviews {
		if r.Body != "" && r.SubmittedAt.After(since) && !isBotComment(r.Body) {
			items = append(items, ReviewItem{Author: r.User.Login, Body: r.Body, At: r.SubmittedAt})
		}
	}

	var general []struct {
		User      struct{ Login string } `json:"user"`
		Body      string                 `json:"body"`
		CreatedAt time.Time              `json:"created_at"`
	}
	if err := g.get(fmt.Sprintf("%s/issues/%d/comments?per_page=100", base, number), &general); err != nil {
		return nil, err
	}
	for _, c := range general {
		if c.CreatedAt.After(since) && !isBotComment(c.Body) {
			items = append(items, ReviewItem{Author: c.User.Login, Body: c.Body, At: c.CreatedAt})
		}
	}
	return items, nil
}

func (g *GitHub) FindPR(owner, name, head, base string) (string, bool, error) {
	var prs []struct {
		HTMLURL string `json:"html_url"`
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls?state=open&head=%s:%s&base=%s", owner, name, owner, head, base)
	if err := g.get(path, &prs); err != nil {
		return "", false, err
	}
	if len(prs) == 0 {
		return "", false, nil
	}
	return prs[0].HTMLURL, true, nil
}

// isBotComment reconoce comentarios automáticos (informes de cobertura, etc.) que no son code review.
func isBotComment(body string) bool {
	return strings.Contains(body, "coverage_comment") || strings.Contains(body, "Code Coverage Report")
}

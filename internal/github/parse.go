package github

import (
	"errors"
	"net"
	"net/url"
	"regexp"
	"strings"
)

const maxRepoInput = 500

// RepoRef is a parsed repository reference from a URL, a deep link or a paste.
type RepoRef struct {
	Host  string
	Owner string
	Name  string
	Tag   string // optional: releases/tag/v1.2.3
}

// HostKind classifies a host so the app knows which API dialect to speak.
func HostKind(host string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	switch {
	case h == "" || h == "github.com":
		return "github"
	case strings.Contains(h, "codeberg.org"), strings.Contains(h, "gitea.com"),
		strings.Contains(h, "forgejo"):
		return "forgejo"
	default:
		return "forgejo"
	}
}

var repoPartRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// ParseRef resolves every supported input form:
//
//	owner/repo
//	https://github.com/owner/repo(.git)
//	https://github.com/owner/repo/releases/tag/v1.2.3
//	https://github.com/owner/repo/tree/main
//	https://codeberg.org/owner/repo
//	git@github.com:owner/repo.git
//	komi://github.com/owner/repo
//	komi://open?url=<encoded>
func ParseRef(input string) (RepoRef, error) {
	raw := strings.TrimSpace(input)
	if raw == "" {
		return RepoRef{}, errors.New("请输入 owner/repo 或仓库链接")
	}
	if len(raw) > maxRepoInput {
		return RepoRef{}, errors.New("输入过长")
	}
	if strings.ContainsAny(raw, "\r\n\t ") {
		fields := strings.Fields(raw)
		if len(fields) == 0 {
			return RepoRef{}, errors.New("输入为空")
		}
		raw = fields[0]
	}

	if strings.HasPrefix(strings.ToLower(raw), "komi://") {
		rest := raw[len("komi://"):]
		if strings.HasPrefix(rest, "open?") {
			u, err := url.Parse("https://placeholder/?" + strings.TrimPrefix(rest, "open?"))
			if err != nil {
				return RepoRef{}, errors.New("无法解析深链参数")
			}
			inner := u.Query().Get("url")
			if inner == "" {
				return RepoRef{}, errors.New("深链缺少 url 参数")
			}
			return ParseRef(inner)
		}
		return ParseRef("https://" + rest)
	}

	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return RepoRef{}, errors.New("链接格式不正确")
		}
		scheme := strings.ToLower(u.Scheme)
		if scheme != "https" && scheme != "http" {
			return RepoRef{}, errors.New("只支持 http/https 链接")
		}
		if scheme == "http" && u.Hostname() != "localhost" {
			return RepoRef{}, errors.New("请使用 https 链接")
		}
		if u.User != nil {
			return RepoRef{}, errors.New("链接不能包含用户名密码")
		}
		host := strings.ToLower(u.Hostname())
		if host == "" || isPrivateHost(host) {
			return RepoRef{}, errors.New("不支持内网或本机地址")
		}
		parts := splitPath(u.Path)
		if len(parts) < 2 {
			return RepoRef{}, errors.New("链接里缺少仓库路径")
		}
		ref := RepoRef{Host: host, Owner: parts[0], Name: strings.TrimSuffix(parts[1], ".git")}
		if len(parts) >= 4 && (parts[2] == "releases" && parts[3] == "tag") && len(parts) >= 5 {
			ref.Tag = parts[4]
		}
		if len(parts) >= 3 && parts[2] == "tags" && len(parts) >= 4 {
			ref.Tag = parts[3]
		}
		return validateRef(ref)
	}

	if strings.HasPrefix(raw, "git@") {
		if i := strings.Index(raw, ":"); i > 0 {
			host := strings.ToLower(raw[len("git@"):i])
			path := strings.TrimSuffix(raw[i+1:], ".git")
			parts := splitPath(path)
			if len(parts) >= 2 {
				return validateRef(RepoRef{Host: host, Owner: parts[0], Name: parts[1]})
			}
		}
		return RepoRef{}, errors.New("无法解析 SSH 链接")
	}

	parts := splitPath(raw)
	if len(parts) < 2 {
		return RepoRef{}, errors.New("请输入 owner/repo 格式")
	}
	return validateRef(RepoRef{Host: "github.com", Owner: parts[0], Name: strings.TrimSuffix(parts[1], ".git")})
}

func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	raw := strings.Split(p, "/")
	out := make([]string, 0, len(raw))
	for _, s := range raw {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func validateRef(ref RepoRef) (RepoRef, error) {
	if ref.Owner == "" || ref.Name == "" {
		return RepoRef{}, errors.New("owner 或 repo 为空")
	}
	if !repoPartRe.MatchString(ref.Owner) || !repoPartRe.MatchString(ref.Name) {
		return RepoRef{}, errors.New("仓库名包含非法字符")
	}
	if ref.Owner == ".." || ref.Name == ".." ||
		strings.Contains(ref.Owner, "..") || strings.Contains(ref.Name, "..") {
		return RepoRef{}, errors.New("仓库名非法")
	}
	if ref.Host == "" {
		ref.Host = "github.com"
	}
	return ref, nil
}

func isPrivateHost(host string) bool {
	h := strings.ToLower(host)
	if h == "localhost" || strings.HasSuffix(h, ".local") || h == "::1" {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
	}
	return false
}

// ParseRepoQuery is the compatibility helper used by tests and old call sites.
// It only accepts github.com inputs and returns owner/repo.
func ParseRepoQuery(input string) (string, string, error) {
	ref, err := ParseRef(input)
	if err != nil {
		return "", "", err
	}
	if ref.Host != "github.com" {
		return "", "", errors.New("这个入口只接受 GitHub 仓库")
	}
	return ref.Owner, ref.Name, nil
}

// RepoKey is the canonical cache/ledger key for GitHub.
func RepoKey(owner, repo string) string {
	return strings.ToLower(owner) + "/" + strings.ToLower(repo)
}

// RepoKeyForHost keeps non-GitHub sources distinguishable without breaking the
// historical "owner/repo" keys already stored in SQLite.
func RepoKeyForHost(host, owner, repo string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "" || h == "github.com" {
		return RepoKey(owner, repo)
	}
	return h + "/" + RepoKey(owner, repo)
}

// SplitRepoKey reverses RepoKeyForHost. sourceHost is "" for GitHub.
func SplitRepoKey(key string) (host, owner, repo string) {
	key = strings.TrimSpace(key)
	if i := strings.Index(key, "#"); i > 0 { // drop the app suffix
		key = key[:i]
	}
	parts := strings.Split(strings.Trim(key, "/"), "/")
	switch len(parts) {
	case 0, 1:
		return "", "", ""
	case 2:
		return "", parts[0], parts[1]
	default:
		host = strings.Join(parts[:len(parts)-2], "/")
		if host == "github.com" {
			host = ""
		}
		return host, parts[len(parts)-2], parts[len(parts)-1]
	}
}

// RepoKeyString builds a key from any parsed ref.
func (r RepoRef) RepoKeyString() string {
	return RepoKeyForHost(r.Host, r.Owner, r.Name)
}

// CheckOwnerRepo validates an already-split pair coming from a form.
func CheckOwnerRepo(owner, repo string) error {
	owner = strings.TrimSpace(owner)
	repo = strings.TrimSpace(repo)
	if owner == "" || repo == "" {
		return errors.New("owner 或 repo 为空")
	}
	if !repoPartRe.MatchString(owner) || !repoPartRe.MatchString(repo) {
		return errors.New("非法的 owner/repo")
	}
	if owner == "." || repo == "." || owner == ".." || repo == ".." ||
		strings.Contains(owner, "..") || strings.Contains(repo, "..") {
		return errors.New("非法的 owner/repo")
	}
	return nil
}

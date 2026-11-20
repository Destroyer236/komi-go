package github

import "testing"

func TestParseRepoQuery(t *testing.T) {
	ok := []struct {
		in          string
		owner, repo string
	}{
		{"owner/repo", "owner", "repo"},
		{" owner/repo ", "owner", "repo"},
		{"https://github.com/owner/repo", "owner", "repo"},
		{"https://github.com/owner/repo.git", "owner", "repo"},
		{"https://github.com/owner/repo/releases/tag/v1.2.3", "owner", "repo"},
		{"git@github.com:owner/repo.git", "owner", "repo"},
		{"owner/repo.git", "owner", "repo"},
	}
	for _, c := range ok {
		o, r, err := ParseRepoQuery(c.in)
		if err != nil {
			t.Errorf("ParseRepoQuery(%q) error: %v", c.in, err)
			continue
		}
		if o != c.owner || r != c.repo {
			t.Errorf("ParseRepoQuery(%q) = %s/%s, want %s/%s", c.in, o, r, c.owner, c.repo)
		}
	}

	bad := []string{"", "   ", "owner", "javascript:alert(1)", "file:///c:/x", "owner/../repo", "own er/repo"}
	for _, in := range bad {
		if _, _, err := ParseRepoQuery(in); err == nil {
			t.Errorf("ParseRepoQuery(%q) should have failed", in)
		}
	}
}

func TestRepoKeyNormalises(t *testing.T) {
	if got := RepoKey("Owner", "Repo"); got != "owner/repo" {
		t.Fatalf("RepoKey = %q", got)
	}
}

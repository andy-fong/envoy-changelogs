package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"golang.org/x/net/html"
)

const (
	summaryField       = "summary "
	areaField          = "area: "
	changeField        = "change: "
	envoyPRBaseUrl     = "https://github.com/envoyproxy/envoy/pull/"
	envoyCommitBaseUrl = "https://github.com/envoyproxy/envoy/commit/"
	changeLogFile      = "changelogs/current.yaml"
	changeLogDir       = "changelogs/current"
)

// sectionOrder matches the section order in changelogs/changelogs.yaml.
var sectionOrder = []string{
	"changes",
	"behavior_changes",
	"minor_behavior_changes",
	"bug_fixes",
	"removed_config_or_runtime",
	"new_features",
	"deprecated",
}

type ChangeLogEntry struct {
	CommitHashes       []string
	PR                 []string
	Category           string
	Area               string
	Summary            []string
	Description        string
	ProcessChangeLines bool
}

type ChangeLogs struct {
	logs             []*ChangeLogEntry
	categoryRegexp   *regexp.Regexp
	prRegexp         *regexp.Regexp
	LastCommitHash   string
	currentCategory  string
	currentEntry     *ChangeLogEntry
	CommitSummaryMap map[string]string
}

func (c *ChangeLogs) addCurrentEntry() {
	if c.currentEntry != nil && len(c.currentEntry.Category) != 0 {
		for _, commitHash := range c.currentEntry.CommitHashes {
			summary := c.CommitSummaryMap[commitHash]
			c.currentEntry.Summary = append(c.currentEntry.Summary, summary)
			pr := c.prRegexp.FindString(summary)
			if len(pr) > 4 {
				c.currentEntry.PR = append(c.currentEntry.PR, pr[2:len(pr)-1])
			} else {
				//				fmt.Printf("warning: cannot find PR from summary: %s\n", c.currentEntry.Summary)
			}
		}
		c.logs = append(c.logs, c.currentEntry)
	}
}

func (c *ChangeLogs) createNewCurrentEntry() {
	c.currentEntry = &ChangeLogEntry{
		Category: c.currentCategory,
	}
	c.currentEntry.CommitHashes = append(c.currentEntry.CommitHashes, c.LastCommitHash)
}

func (c *ChangeLogs) ProcessContent(line string) {
	// line = strings.TrimLeft(line, "\t")
	//	fmt.Printf("processing: %s\n", line)

	if len(line) == 0 {
		return
	}
	if line == c.categoryRegexp.FindString(line) {
		c.currentCategory = strings.TrimRight(line, ":")
		//		fmt.Printf("found category: %s\n", c.currentCategory)
		return
	}

	if strings.HasPrefix(line, "- ") {
		c.addCurrentEntry()
		c.createNewCurrentEntry()
	}

	if c.currentEntry == nil {
		return
	}

	commitHashAlreadyExists := false
	for _, commitHash := range c.currentEntry.CommitHashes {
		if commitHash == c.LastCommitHash {
			commitHashAlreadyExists = true
		}
	}
	if !commitHashAlreadyExists {
		c.currentEntry.CommitHashes = append(c.currentEntry.CommitHashes, c.LastCommitHash)
	}
	line = strings.TrimLeft(line, "- ")

	if c.currentEntry.ProcessChangeLines {
		c.currentEntry.Description += line + " "
		return
	}

	if strings.HasPrefix(line, areaField) {
		c.currentEntry.Area = line[len(areaField):]
		return
	}

	if strings.HasPrefix(line, changeField) {
		c.currentEntry.ProcessChangeLines = true
		return
	}
}

func (c *ChangeLogs) ProcessGitBlameOutput(br *git.BlameResult) {
}

func getReferenceLinks(url string) map[string]string {
	refMap := make(map[string]string)
	resp, err := http.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error fetching URL: %v\n", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "%s returns status code %d\n", url, resp.StatusCode)
		return nil
	}

	ctype := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ctype, "text/html") {
		fmt.Fprintf(os.Stderr, "response content type was %s not text/html\n", ctype)
		return nil
	}

	tokenizer := html.NewTokenizer(resp.Body)
	for {
		tt := tokenizer.Next()
		if tt == html.ErrorToken {
			if tokenizer.Err() == io.EOF {
				return refMap
			}
			fmt.Fprintf(os.Stderr, "Error: %v", tokenizer.Err())
			return nil
		}
		tag, hasAttr := tokenizer.TagName()
		if string(tag) == "a" && hasAttr {
			isRefClass := false
			href := ""
			for {
				attrKey, attrValue, moreAttr := tokenizer.TagAttr()
				if string(attrKey) == "class" && strings.HasPrefix(string(attrValue), "reference") {
					isRefClass = true
				}
				if string(attrKey) == "href" {
					href = string(attrValue)
				}

				if !moreAttr {
					if isRefClass && len(href) > 0 {
						for tokenizer.Next() != html.TextToken {
							refMap[tokenizer.Token().Data] = href
						}
						refMap[tokenizer.Token().Data] = href
					}
					break
				}
			}
		}
	}

}

func getCommitSummary(commit *object.Commit) string {
	lines := strings.Split(commit.Message, "\n")
	if len(lines) == 0 {
		return ""
	}

	return strings.TrimSpace(lines[0])
}

func isErrReferenceNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "reference not found")
}

// readWorktreeHead handles linked worktrees where go-git's repo.Head() fails.
// A linked worktree has .git as a file pointing to .git/worktrees/<name>;
// HEAD is stored there rather than in the main .git directory.
func readWorktreeHead(repoDir string) (string, plumbing.Hash, error) {
	gitFilePath := filepath.Join(repoDir, ".git")
	fi, err := os.Stat(gitFilePath)
	if err != nil {
		return "", plumbing.ZeroHash, fmt.Errorf("cannot stat .git: %w", err)
	}
	if fi.IsDir() {
		return "", plumbing.ZeroHash, fmt.Errorf("not a linked worktree: .git is a directory")
	}

	data, err := os.ReadFile(gitFilePath)
	if err != nil {
		return "", plumbing.ZeroHash, err
	}
	gitdir := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(data)), "gitdir: "))
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(repoDir, gitdir)
	}

	headData, err := os.ReadFile(filepath.Join(gitdir, "HEAD"))
	if err != nil {
		return "", plumbing.ZeroHash, err
	}
	headStr := strings.TrimSpace(string(headData))

	if strings.HasPrefix(headStr, "ref: ") {
		ref := strings.TrimPrefix(headStr, "ref: ")
		branchName := filepath.Base(ref)
		// gitdir is .git/worktrees/<name>; main git dir is two levels up
		mainGitDir := filepath.Clean(filepath.Join(gitdir, "..", ".."))
		hash, err := resolveGitRef(mainGitDir, ref)
		if err != nil {
			return "", plumbing.ZeroHash, err
		}
		return branchName, hash, nil
	}

	// Detached HEAD
	return headStr[:8], plumbing.NewHash(headStr), nil
}

// findMainWorkDir follows the .git file and commondir to locate the main repo's working directory.
func findMainWorkDir(repoDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(repoDir, ".git"))
	if err != nil {
		return "", err
	}
	gitdir := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(data)), "gitdir: "))
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(repoDir, gitdir)
	}

	// commondir (if present) points to the main .git dir relative to gitdir
	commondirData, err := os.ReadFile(filepath.Join(gitdir, "commondir"))
	if err != nil {
		// No commondir: gitdir itself is the main .git dir
		return filepath.Dir(gitdir), nil
	}
	commondir := strings.TrimSpace(string(commondirData))
	if !filepath.IsAbs(commondir) {
		commondir = filepath.Join(gitdir, commondir)
	}
	return filepath.Dir(filepath.Clean(commondir)), nil
}

func resolveGitRef(gitDir, ref string) (plumbing.Hash, error) {
	// Try loose ref
	refPath := filepath.Join(gitDir, filepath.FromSlash(ref))
	if data, err := os.ReadFile(refPath); err == nil {
		return plumbing.NewHash(strings.TrimSpace(string(data))), nil
	}

	// Try packed-refs
	packedRefs, err := os.ReadFile(filepath.Join(gitDir, "packed-refs"))
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("ref %s not found", ref)
	}
	for _, line := range strings.Split(string(packedRefs), "\n") {
		if strings.HasSuffix(line, " "+ref) {
			if parts := strings.Fields(line); len(parts) >= 2 {
				return plumbing.NewHash(parts[0]), nil
			}
		}
	}
	return plumbing.ZeroHash, fmt.Errorf("ref %s not found in packed-refs", ref)
}

func runGit(repoDir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", repoDir}, args...)...)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// rstToText joins wrapped lines into single lines, keeping blank-line paragraph breaks.
func rstToText(content string) string {
	var paragraphs []string
	var current []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if len(line) == 0 {
			if len(current) > 0 {
				paragraphs = append(paragraphs, strings.Join(current, " "))
				current = nil
			}
			continue
		}
		current = append(current, line)
	}
	if len(current) > 0 {
		paragraphs = append(paragraphs, strings.Join(current, " "))
	}
	return strings.Join(paragraphs, "  \n\n")
}

func sectionRank(section string) int {
	for i, s := range sectionOrder {
		if s == section {
			return i
		}
	}
	return len(sectionOrder)
}

// collectEntryFiles builds changelog entries from changelogs/current/<section>/<area>__<slug>.rst.
// The commits for each entry are every commit that touched the file (following renames)
// back to the commit that added it.
// It also returns the commits that moved entries out of changelogs/current.yaml into this layout.
func collectEntryFiles(repoDir, rev string, prRegexp *regexp.Regexp) ([]*ChangeLogEntry, []string, error) {
	out, err := runGit(repoDir, "ls-tree", "-r", "--name-only", rev, "--", changeLogDir+"/")
	if err != nil {
		return nil, nil, err
	}

	// tracked maps the path a file has at the commit being examined to its entry.
	tracked := make(map[string]*ChangeLogEntry)
	var logs []*ChangeLogEntry
	for _, path := range strings.Split(strings.TrimSpace(out), "\n") {
		if !strings.HasSuffix(path, ".rst") {
			continue
		}
		parts := strings.Split(strings.TrimPrefix(path, changeLogDir+"/"), "/")
		if len(parts) != 2 {
			fmt.Fprintf(os.Stderr, "warning: unexpected changelog path: %s\n", path)
			continue
		}
		area, _, _ := strings.Cut(strings.TrimSuffix(parts[1], ".rst"), "__")
		content, err := runGit(repoDir, "show", rev+":"+path)
		if err != nil {
			return nil, nil, err
		}
		entry := &ChangeLogEntry{
			Category:    parts[0],
			Area:        strings.ReplaceAll(area, "~", "/"),
			Description: rstToText(content),
		}
		tracked[path] = entry
		logs = append(logs, entry)
	}

	var migrationCommits []string
	out, err = runGit(repoDir, "log", "-M", "--name-status", "--format=%x00%H%x09%s", rev, "--", changeLogDir+"/", changeLogFile)
	if err != nil {
		return nil, nil, err
	}
	for _, block := range strings.Split(out, "\x00") {
		lines := strings.Split(strings.TrimSpace(block), "\n")
		hash, summary, ok := strings.Cut(lines[0], "\t")
		if !ok {
			continue
		}
		touchesChangeLogFile := slices.ContainsFunc(lines[1:], func(line string) bool {
			return strings.HasSuffix(line, "\t"+changeLogFile)
		})
		for _, line := range lines[1:] {
			fields := strings.Split(line, "\t")
			if len(fields) < 2 {
				continue
			}
			path := fields[len(fields)-1]
			entry := tracked[path]
			if entry == nil {
				continue
			}
			entry.CommitHashes = append(entry.CommitHashes, hash)
			entry.Summary = append(entry.Summary, summary)
			if pr := prRegexp.FindString(summary); len(pr) > 4 {
				entry.PR = append(entry.PR, pr[2:len(pr)-1])
			}
			delete(tracked, path)
			switch fields[0][0] {
			case 'R':
				tracked[fields[1]] = entry
			case 'M':
				tracked[path] = entry
			}
			// 'A' ends the history of this entry. If the same commit changed current.yaml,
			// the entry was moved from there rather than newly written.
			if fields[0] == "A" && touchesChangeLogFile && !slices.Contains(migrationCommits, hash) {
				migrationCommits = append(migrationCommits, hash)
			}
		}
	}

	for _, entry := range logs {
		// git log is newest first; list the commit that added the entry first.
		slices.Reverse(entry.CommitHashes)
		slices.Reverse(entry.Summary)
		slices.Reverse(entry.PR)
	}
	slices.SortStableFunc(logs, func(a, b *ChangeLogEntry) int {
		return sectionRank(a.Category) - sectionRank(b.Category)
	})
	return logs, migrationCommits, nil
}

func normalizeDescription(s string) string {
	return strings.Join(strings.Fields(s), "")
}

// mergeMigratedEntries prepends the history from the pre-migration current.yaml to entries
// that were created by the migration commit, so they link to the PR that introduced them.
func mergeMigratedEntries(logs, oldLogs []*ChangeLogEntry, migrationCommit string) {
	used := make(map[*ChangeLogEntry]bool)
	find := func(entry *ChangeLogEntry, match func(old, cur string) bool) *ChangeLogEntry {
		cur := normalizeDescription(entry.Description)
		for _, old := range oldLogs {
			if !used[old] && old.Category == entry.Category && old.Area == entry.Area &&
				match(normalizeDescription(old.Description), cur) {
				return old
			}
		}
		return nil
	}
	for _, entry := range logs {
		if len(entry.CommitHashes) == 0 || entry.CommitHashes[0] != migrationCommit {
			continue
		}
		old := find(entry, func(old, cur string) bool { return old == cur })
		if old == nil {
			// The text may have been touched up during migration; fall back to a prefix match.
			old = find(entry, func(old, cur string) bool {
				n := min(len(old), len(cur), 40)
				return old[:n] == cur[:n]
			})
		}
		if old == nil {
			fmt.Fprintf(os.Stderr, "warning: no pre-migration entry found for %s/%s\n", entry.Category, entry.Area)
			continue
		}
		used[old] = true
		entry.CommitHashes = append(old.CommitHashes, entry.CommitHashes...)
		entry.Summary = append(old.Summary, entry.Summary...)
		entry.PR = append(old.PR, entry.PR...)
	}
}

// blameChangeLogFile collects entries from changelogs/current.yaml at the given commit.
func blameChangeLogFile(repo *git.Repository, c *object.Commit) *ChangeLogs {
	br, err := git.Blame(c, changeLogFile)
	if err != nil {
		panic(err)
	}

	changeLogs := &ChangeLogs{
		categoryRegexp:   regexp.MustCompile(`^[a-z_]+:$`),
		prRegexp:         regexp.MustCompile(`\(#[0-9]+\)`),
		CommitSummaryMap: make(map[string]string),
	}
	for _, line := range br.Lines {
		changeLogs.LastCommitHash = line.Hash.String()
		changeLogs.ProcessContent(line.Text)
		commit, _ := repo.CommitObject(line.Hash)
		changeLogs.CommitSummaryMap[changeLogs.LastCommitHash] = getCommitSummary(commit)
	}
	// Takes care of the last outstanding entry
	changeLogs.addCurrentEntry()
	return changeLogs
}

// openRepo opens the repository, using the main repo's object store for linked worktrees.
func openRepo(repoDir string) (*git.Repository, error) {
	if fi, err := os.Stat(filepath.Join(repoDir, ".git")); err == nil && !fi.IsDir() {
		mainWorkDir, err := findMainWorkDir(repoDir)
		if err != nil {
			return nil, err
		}
		return git.PlainOpen(mainWorkDir)
	}
	return git.PlainOpenWithOptions(repoDir, &git.PlainOpenOptions{DetectDotGit: true})
}

// openRepoAtHead opens the repository and returns the checked out branch name and commit.
func openRepoAtHead(repoDir string) (*git.Repository, string, plumbing.Hash, error) {
	options := &git.PlainOpenOptions{
		DetectDotGit: true,
	}
	repo, err := git.PlainOpenWithOptions(repoDir, options)
	if err != nil {
		return nil, "", plumbing.ZeroHash, err
	}

	h, err := repo.Head()
	if err == nil {
		return repo, h.Name().Short(), h.Hash(), nil
	}
	if !isErrReferenceNotFound(err) {
		return nil, "", plumbing.ZeroHash, err
	}

	// go-git doesn't support linked worktrees; read HEAD from the worktree gitdir directly
	// and reopen the main repo so CommitObject/Blame can access the full object store.
	version, commitHash, err := readWorktreeHead(repoDir)
	if err != nil {
		return nil, "", plumbing.ZeroHash, err
	}
	repo, err = openRepo(repoDir)
	if err != nil {
		return nil, "", plumbing.ZeroHash, err
	}
	return repo, version, commitHash, nil
}

func main() {
	if len(os.Args) != 2 && len(os.Args) != 3 {
		err := fmt.Errorf("usage: %s <envoy_repo_directory> [version_tag]", os.Args[0])
		panic(err)
	}

	repoDir := os.Args[1]
	var repo *git.Repository
	var err error
	var version string
	var commitHash plumbing.Hash
	if len(os.Args) == 3 {
		// Use the release tag directly; the checked out branch doesn't matter.
		version = os.Args[2]
		if !strings.HasPrefix(version, "v") {
			version = "v" + version
		}
		out, err := runGit(repoDir, "rev-parse", "--verify", "refs/tags/"+version+"^{commit}")
		if err != nil {
			panic(fmt.Errorf("cannot find tag %s: %w", version, err))
		}
		commitHash = plumbing.NewHash(strings.TrimSpace(out))
		repo, err = openRepo(repoDir)
		if err != nil {
			panic(err)
		}
	} else {
		repo, version, commitHash, err = openRepoAtHead(repoDir)
		if err != nil {
			panic(err)
		}
	}

	// Starting with v1.39, envoy keeps one file per entry in
	// changelogs/current/<section>/<area>__<slug>.rst instead of current.yaml.
	out, err := runGit(repoDir, "ls-tree", "--name-only", commitHash.String(), "--", changeLogFile, changeLogDir)
	if err != nil {
		panic(err)
	}
	found := strings.Fields(out)
	useEntryDir := false
	if !slices.Contains(found, changeLogFile) {
		if !slices.Contains(found, changeLogDir) {
			panic(fmt.Errorf("Neither %s nor %s/ exists in %s", changeLogFile, changeLogDir, version))
		}
		useEntryDir = true
	}

	if !strings.HasPrefix(version, "v") {
		if idx := strings.Index(version, "v"); idx != -1 {
			version = version[idx:]
		}
	}

	// fmt.Fprintf(os.Stderr, "version: %s\n", version)

	c, err := repo.CommitObject(commitHash)
	if err != nil {
		panic(err)
	}

	var changeLogs *ChangeLogs
	if useEntryDir {
		changeLogs = &ChangeLogs{}
		var migrationCommits []string
		changeLogs.logs, migrationCommits, err = collectEntryFiles(repoDir, commitHash.String(), regexp.MustCompile(`\(#[0-9]+\)`))
		if err != nil {
			panic(err)
		}
		for _, migrationCommit := range migrationCommits {
			mc, err := repo.CommitObject(plumbing.NewHash(migrationCommit))
			if err != nil {
				panic(err)
			}
			parent, err := mc.Parent(0)
			if err != nil {
				panic(err)
			}
			mergeMigratedEntries(changeLogs.logs, blameChangeLogFile(repo, parent).logs, migrationCommit)
		}
	} else {
		changeLogs = blameChangeLogFile(repo, c)
	}

	envoyhost := "https://www.envoyproxy.io"
	lastDotIndex := strings.LastIndex(version, ".")
	majorminor := version[0:lastDotIndex]
	baseUrl := envoyhost + "/docs/envoy/latest/version_history/" + majorminor + "/"
	refMap := getReferenceLinks(baseUrl + version)
	//	fmt.Fprintf(os.Stderr, "refMap:\n%v", refMap)
	refRegexp := regexp.MustCompile(":ref:`([^`]+) *(<[^`]*)`")
	optionRegexp := regexp.MustCompile(":option:`([^`]*)`")
	fmt.Printf("# Envoy Release %s\n\n", version)

	fmt.Printf("[release note](%s%s)\n\n", baseUrl, version)
	currentCategory := ""
	for _, entry := range changeLogs.logs {
		if currentCategory != entry.Category {
			currentCategory = entry.Category
			fmt.Printf("## %s\n\n", currentCategory)
		}
		fmt.Printf("**category**   : %s  \n", entry.Category)
		fmt.Printf("**area**       : %s  \n", entry.Area)
		for _, summary := range entry.Summary {
			fmt.Printf("**summary**    : %v  \n", summary)
		}
		fmt.Printf("**commit**     : ")
		for _, commit := range entry.CommitHashes {
			fmt.Printf("[%v](%v%v) ", commit, envoyCommitBaseUrl, commit)
		}
		fmt.Printf(" \n")
		//		fmt.Printf("%v %v\n", entry.CommitHashes, entry.PR)
		fmt.Printf("**pr**         : ")
		for _, pr := range entry.PR {
			fmt.Printf("[%v](%v%v) ", pr, envoyPRBaseUrl, pr)
		}
		fmt.Printf(" \n")
		description := refRegexp.ReplaceAllStringFunc(entry.Description, func(s string) string {
			refMatches := refRegexp.FindAllStringSubmatch(s, -1)
			key := strings.TrimRight(refMatches[0][1], " ")
			if len(refMap[key]) == 0 {
				return "`" + key + "`"
			}
			return "[" + key + "](" + refMap[key] + ")"
		})
		description = optionRegexp.ReplaceAllStringFunc(description, func(s string) string {
			refMatches := optionRegexp.FindAllStringSubmatch(s, -1)
			key := refMatches[0][1]
			return "[" + key + "](" + envoyhost + refMap[key] + ")"
		})
		//fmt.Printf("description:\n%s\n", entry.Description)
		fmt.Printf("**description**:  \n%s  \n", description)
		//		fmt.Printf("detected Change: %v\n", entry.ProcessChangeLines)
		fmt.Printf(" \n")
		fmt.Println("---")
		fmt.Printf("\n")
	}
}

package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

const (
	summaryField = "summary "
	areaField    = "area: "
	changeField  = "change: "
)

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
	hexRegexp        *regexp.Regexp
	categoryRegexp   *regexp.Regexp
	prRegexp         *regexp.Regexp
	lastCommitHash   string
	currentCategory  string
	currentEntry     *ChangeLogEntry
	commitSummaryMap map[string]string
}

func (c *ChangeLogs) addCurrentEntry() {
	if c.currentEntry != nil && len(c.currentEntry.Category) != 0 {
		for _, commitHash := range c.currentEntry.CommitHashes {
			summary := c.commitSummaryMap[commitHash]
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
	c.currentEntry.CommitHashes = append(c.currentEntry.CommitHashes, c.lastCommitHash)
}

func (c *ChangeLogs) ProcessContent(line string) {
	line = strings.TrimLeft(line, "\t")
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
		if commitHash == c.lastCommitHash {
			commitHashAlreadyExists = true
		}
	}
	if !commitHashAlreadyExists {
		c.currentEntry.CommitHashes = append(c.currentEntry.CommitHashes, c.lastCommitHash)
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

func (c *ChangeLogs) ProcessGitBlameOutput(line string) {
	if strings.HasPrefix(line, "\t") {
		c.ProcessContent(line)
		return
	} else {
		parts := strings.SplitN(line, " ", 2)
		if parts[0] == c.hexRegexp.FindString(parts[0]) {
			c.lastCommitHash = parts[0]
			return
		}
	}

	if strings.HasPrefix(line, summaryField) {
		summary := line[len(summaryField):]
		// fmt.Printf("%s: %s\n", c.lastCommitHash, summary)
		c.commitSummaryMap[c.lastCommitHash] = summary
	}
}

func main() {
	if len(os.Args) != 2 {
		err := fmt.Errorf("usage: %s <changelog_file>", os.Args[0])
		panic(err)
	}

	changeLogFile := os.Args[1]
	if _, err := os.Stat(changeLogFile); err != nil {
		if os.IsNotExist(err) {
			err = fmt.Errorf("File %s does not exist", changeLogFile)
		} else {
			err = fmt.Errorf("Error checking file %s:", changeLogFile, err)
		}
		panic(err)
	}

	cmd := exec.Command("git", "blame", "-p", changeLogFile)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		panic(err)
	}

	if err := cmd.Start(); err != nil {
		panic(err)
	}

	scanner := bufio.NewScanner(stdout)
	changeLogs := &ChangeLogs{
		categoryRegexp:   regexp.MustCompile(`^[a-z_]+:$`),
		hexRegexp:        regexp.MustCompile(`[0-9a-f]+`),
		prRegexp:         regexp.MustCompile(`\(#[0-9]+\)`),
		commitSummaryMap: make(map[string]string),
	}

	for scanner.Scan() {
		changeLogs.ProcessGitBlameOutput(scanner.Text())
	}
	// Takes care of the last outstanding entry
	changeLogs.addCurrentEntry()

	if err := cmd.Wait(); err != nil {
		panic(err)
	}

	for _, entry := range changeLogs.logs {
		fmt.Printf("commit: %v\n", entry.CommitHashes)
		fmt.Printf("pr: %v\n", entry.PR)
		fmt.Printf("category: %s\n", entry.Category)
		fmt.Printf("area: %s\n", entry.Area)
		fmt.Printf("summary: %v\n", entry.Summary)
		fmt.Printf("description:\n%s\n", entry.Description)
		//		fmt.Printf("detected Change: %v\n", entry.ProcessChangeLines)
		fmt.Println("----------------------------------------------------------------")
	}
}

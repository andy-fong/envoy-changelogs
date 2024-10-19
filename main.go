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
	CommitHash         []byte
	PR                 string
	Category           string
	Area               string
	Summary            string
	Description        string
	ProcessChangeLines bool
}

type ChangeLogs struct {
	logs              []*ChangeLogEntry
	hexRegexp         *regexp.Regexp
	categoryRegexp    *regexp.Regexp
	prRegexp          *regexp.Regexp
	currentCommitHash string
	currentCategory   string
	currentEntry      *ChangeLogEntry
}

func (c *ChangeLogs) ProcessContent(entry *ChangeLogEntry, line string) {
	line = strings.TrimLeft(line, "\t")
	//	fmt.Printf("processing: %s\n", line)
	if line == c.categoryRegexp.FindString(line) {
		c.currentCategory = strings.TrimRight(line, ":")
		fmt.Printf("found category: %s\n", c.currentCategory)
		return
	}

	line = strings.TrimLeft(line, "- ")

	if entry.ProcessChangeLines {
		entry.Description += line + " "
		return
	}

	if strings.HasPrefix(line, areaField) {
		entry.Area = line[len(areaField):]
	}

	if strings.HasPrefix(line, changeField) {
		entry.ProcessChangeLines = true
	}
}

func (c *ChangeLogs) ProcessGitBlameOutput(line string) {
	parts := strings.SplitN(line, " ", 2)
	if parts[0] == c.hexRegexp.FindString(parts[0]) && parts[0] != c.currentCommitHash {
		if c.currentEntry != nil && len(c.currentEntry.Category) != 0 {
			fmt.Println(c.currentCommitHash)
			c.logs = append(c.logs, c.currentEntry)
		}
		c.currentCommitHash = parts[0]
		c.currentEntry = &ChangeLogEntry{
			CommitHash: []byte(c.currentCommitHash[0:11]),
			Category:   c.currentCategory,
		}
	}

	if strings.HasPrefix(line, "\t") {
		c.ProcessContent(c.currentEntry, line)
	}

	if len(c.currentCategory) == 0 || c.currentEntry == nil {
		return
	}

	if strings.HasPrefix(line, summaryField) {
		c.currentEntry.Summary = line[len(summaryField):]
		pr := c.prRegexp.FindString(c.currentEntry.Summary)
		if len(pr) > 4 {
			c.currentEntry.PR = pr[2 : len(pr)-1]
		} else {
			fmt.Printf("warning: cannot find PR from summary: %s\n", c.currentEntry.Summary)
		}
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
		categoryRegexp: regexp.MustCompile(`^[a-z]+:$`),
		hexRegexp:      regexp.MustCompile(`[0-9a-f]+`),
		prRegexp:       regexp.MustCompile(`\(#[0-9]+\)`),
	}

	for scanner.Scan() {
		changeLogs.ProcessGitBlameOutput(scanner.Text())
	}

	if err := cmd.Wait(); err != nil {
		panic(err)
	}

	for _, entry := range changeLogs.logs {
		fmt.Printf("commit: %s\n", entry.CommitHash)
		fmt.Printf("pr: %s\n", entry.PR)
		fmt.Printf("category: %s\n", entry.Category)
		fmt.Printf("area: %s\n", entry.Area)
		fmt.Printf("summary: %s\n", entry.Summary)
		fmt.Printf("description:\n%s\n", entry.Description)
		fmt.Printf("detected Change: %v\n", entry.ProcessChangeLines)
		fmt.Println("----------------------------------------------------------------")
	}
}

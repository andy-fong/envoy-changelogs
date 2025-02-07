package main

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

const (
	summaryField       = "summary "
	areaField          = "area: "
	changeField        = "change: "
	envoyPRBaseUrl     = "https://github.com/envoyproxy/envoy/pull/"
	envoyCommitBaseUrl = "https://github.com/envoyproxy/envoy/commit/"
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
		fmt.Printf("line0: %s\n", line)
		c.ProcessContent(line)
		return
	} else {
		fmt.Printf("line1: %s\n", line)
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

func writeCSV(changeLogs *ChangeLogs, filename string) error {
	f, err := os.Create(filename)
	if err != nil {
		return err
	}
	writer := csv.NewWriter(f)

	for _, entry := range changeLogs.logs {
		writer.Write(entry.CommitHashes)
	}

	return nil
}

func getReferenceLinks(url string) map[string]string {
	refMap := make(map[string]string)
	resp, err := http.Get(url)
	if err != nil {
		fmt.Printf("error fetching URL: %v\n", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Printf("response status code was %d\n", resp.StatusCode)
		return nil
	}

	ctype := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ctype, "text/html") {
		fmt.Printf("response content type was %s not text/html\n", ctype)
		return nil
	}

	tokenizer := html.NewTokenizer(resp.Body)
	for {
		tt := tokenizer.Next()
		if tt == html.ErrorToken {
			if tokenizer.Err() == io.EOF {
				return refMap
			}
			fmt.Printf("Error: %v", tokenizer.Err())
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

	envoyhost := "https://www.envoyproxy.io"
	version := "v1.33.0"
	lastDotIndex := strings.LastIndex(version, ".")
	majorminor := version[0:lastDotIndex]
	baseUrl := envoyhost + "/docs/envoy/latest/version_history/" + majorminor + "/"
	refMap := getReferenceLinks(baseUrl + version)
	// fmt.Printf("refMap:\n%v", refMap)
	refRegexp := regexp.MustCompile(":ref:`([_a-zA-Z0-9%]+)[^`]*`")
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
			key := refMatches[0][1]
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

package dashboard

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

// DeadlineItem is one parsed line from the deadlines file — display only, no
// date math, no caching. Same regex cmd_face.go's nextDeadlineLine already
// uses (duplicated here rather than imported, since that one lives in package
// main); kept in sync by hand if the format changes. Urgent is a straight
// string match against the file's OWN "**OVERDUE-SOON**" marker — that
// classification is computed by whatever tool maintains the file; thaw just
// reads whether the marker is present. Never a date comparison thaw does
// itself.
type DeadlineItem struct {
	Date   string
	Text   string
	Urgent bool
}

// DeadlinesFile returns the path to the user's deadlines markdown file, from
// the THAW_DEADLINES_FILE environment variable. Empty means the feature is
// off entirely — no panel, no read, no default location guessed at.
func DeadlinesFile() string {
	return os.Getenv("THAW_DEADLINES_FILE")
}

var deadlineLineRe = regexp.MustCompile(`^- (\d{4}-\d{2}-\d{2})\s+(.+?)\s*(?:\*\*|\[doc|\[dl|$)`)

// NextDeadlines reads up to n upcoming items from the deadlines file, live,
// every call — no cache, no computed dates. Deadline math is deliberately out
// of scope; this only ever displays what the file already says, verbatim,
// labeled as unverified since thaw has no way to check the dates itself.
func NextDeadlines(path string, n int) []DeadlineItem {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var items []DeadlineItem
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		m := deadlineLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		items = append(items, DeadlineItem{
			Date:   m[1],
			Text:   strings.TrimSpace(m[2]),
			Urgent: strings.Contains(line, "OVERDUE-SOON"),
		})
		if len(items) >= n {
			break
		}
	}
	return items
}

// DeadlinesFreshness returns how long ago the deadlines file was last
// modified — thaw doesn't verify deadlines (that's the job of whatever tool
// writes the file), but it CAN honestly say how old the file it's reading
// is. In scope because it's a file mtime read, not deadline math. Returns
// false if the file can't be stat'd.
func DeadlinesFreshness(path string) (time.Time, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}, false
	}
	return info.ModTime(), true
}

// Line renders one deadline with the same accuracy caveat thaw face uses.
func (d DeadlineItem) Line() string {
	return fmt.Sprintf("%s %s (unverified)", d.Date, d.Text)
}

package downloader

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// extractLecturerName finds the lecturer name from various XML sources.
// This is a fallback when user mapping from indexstream.xml is unavailable.
// It checks multiple patterns in the raw recording files.
func extractLecturerName(rawDir string) string {
	// First try: transcriptstream.xml for "[Tech - Name]" or similar patterns
	transcriptPath := filepath.Join(rawDir, "transcriptstream.xml")
	if f, err := os.Open(transcriptPath); err == nil {
		defer f.Close()
		scanner := bufio.NewScanner(f)
		// Look for pattern: [Tech - Name] or [Tech  Name] has joined the stage
		// Matches "Tech" followed by optional dash/spaces and the name
		techRe := regexp.MustCompile(`\[Tech\s*[-–]?\s*([^\]]+)\]`)

		for scanner.Scan() {
			line := scanner.Text()
			if strings.Contains(line, "has joined the stage") {
				if match := techRe.FindStringSubmatch(line); len(match) >= 2 {
					return strings.TrimSpace(match[1])
				}
			}
		}
	}

	// Second try: fttitle*.xml for "Lecturer:" pattern
	files, _ := filepath.Glob(filepath.Join(rawDir, "fttitle*.xml"))
	// Match "Lecturer:" followed by optional HTML tags and the actual name
	lecturerRe := regexp.MustCompile(`Lecturer:\s*(?:<[^>]+>\s*)*([A-Z][a-zA-Z]+(?:\s+[A-Z][a-zA-Z]+)+)`)

	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}

		if match := lecturerRe.FindStringSubmatch(string(data)); len(match) >= 2 {
			return strings.TrimSpace(match[1])
		}
	}

	return ""
}

// extractUserMapping parses indexstream.xml to get the mapping from anonymous IDs to real names.
// Returns a map like {"User1": "Jane Smith", "User13": "John Doe", ...}.
func extractUserMapping(rawDir string) map[string]string {
	mapping := make(map[string]string)

	indexPath := filepath.Join(rawDir, "indexstream.xml")
	data, err := os.ReadFile(indexPath)
	if err != nil {
		return mapping
	}

	content := string(data)

	// Match pairs of anonymousName and fullName
	anonRe := regexp.MustCompile(`<anonymousName><!\[CDATA\[([^\]]+)\]\]></anonymousName>`)
	nameRe := regexp.MustCompile(`<fullName><!\[CDATA\[([^\]]*)\]\]></fullName>`)

	anonMatches := anonRe.FindAllStringSubmatchIndex(content, -1)
	nameMatches := nameRe.FindAllStringSubmatch(content, -1)

	// Match by order - each anonymousName is followed by its fullName
	for i, anonMatch := range anonMatches {
		if i < len(nameMatches) {
			anonName := content[anonMatch[2]:anonMatch[3]]
			fullName := nameMatches[i][1]
			if fullName != "" {
				// Clean up "Tech " prefix from lecturer names
				fullName = strings.TrimPrefix(fullName, "Tech ")
				fullName = strings.TrimSpace(fullName)
				mapping[anonName] = fullName
			}
		}
	}

	return mapping
}

// extractChatLog parses transcriptstream.xml and creates a readable chat log.
func extractChatLog(rawDir, outputPath string) error {
	transcriptPath := filepath.Join(rawDir, "transcriptstream.xml")
	f, err := os.Open(transcriptPath)
	if err != nil {
		return err
	}
	defer f.Close()

	out, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer out.Close()

	fmt.Fprintf(out, "CHAT LOG\n")
	fmt.Fprintf(out, "========\n\n")

	scanner := bufio.NewScanner(f)
	// Regex patterns for extracting chat data
	iconTypeRe := regexp.MustCompile(`<iconType><!\[CDATA\[([^\]]+)\]\]></iconType>`)
	labelRe := regexp.MustCompile(`<label><!\[CDATA\[([^\]]*)\]\]></label>`)
	nameRe := regexp.MustCompile(`<name><!\[CDATA\[([^\]]*)\]\]></name>`)
	timeRe := regexp.MustCompile(`<time><!\[CDATA\[(\d+)\]\]></time>`)

	var currentIconType, currentLabel, currentName string
	var currentTime int64

	for scanner.Scan() {
		line := scanner.Text()

		if match := iconTypeRe.FindStringSubmatch(line); len(match) >= 2 {
			currentIconType = match[1]
		}
		if match := labelRe.FindStringSubmatch(line); len(match) >= 2 {
			currentLabel = match[1]
		}
		if match := nameRe.FindStringSubmatch(line); len(match) >= 2 {
			currentName = match[1]
		}
		if match := timeRe.FindStringSubmatch(line); len(match) >= 2 {
			_, _ = fmt.Sscanf(match[1], "%d", &currentTime)
		}

		// When we hit closing </Object>, output the chat message
		if strings.Contains(line, "</Object>") && currentIconType == "chat" && currentLabel != "" {
			timestamp := formatMilliseconds(currentTime)
			if currentName != "" {
				fmt.Fprintf(out, "[%s] %s: %s\n", timestamp, currentName, currentLabel)
			} else {
				fmt.Fprintf(out, "[%s] %s\n", timestamp, currentLabel)
			}
			currentIconType = ""
			currentLabel = ""
			currentName = ""
		}
	}

	return scanner.Err()
}

// formatMilliseconds converts milliseconds to HH:MM:SS format.
func formatMilliseconds(ms int64) string {
	secs := ms / 1000
	hours := secs / 3600
	mins := (secs % 3600) / 60
	seconds := secs % 60
	return fmt.Sprintf("%02d:%02d:%02d", hours, mins, seconds)
}

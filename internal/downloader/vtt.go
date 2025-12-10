package downloader

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// vttToTranscript converts a VTT file to a readable plain text transcript.
// The output is formatted with timestamps and speaker text, suitable for AI tools.
func vttToTranscript(vttPath, outputPath string) error {
	f, err := os.Open(vttPath)
	if err != nil {
		return err
	}
	defer f.Close()

	out, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer out.Close()

	scanner := bufio.NewScanner(f)
	var (
		inCue       bool
		lastText    string
		cueStart    string
		lineCount   int
		wroteHeader bool
	)

	// VTT format:
	// WEBVTT
	//
	// 00:00:00.000 --> 00:00:05.000
	// Speaker text here
	//
	// 00:00:05.000 --> 00:00:10.000
	// More text here

	timeRe := regexp.MustCompile(`^(\d{2}:\d{2}:\d{2})[.,]\d{3}\s*-->\s*\d{2}:\d{2}:\d{2}`)

	for scanner.Scan() {
		line := scanner.Text()
		lineCount++

		// Skip WEBVTT header and metadata
		if lineCount == 1 && strings.HasPrefix(line, "WEBVTT") {
			continue
		}

		// Check for timestamp line
		if match := timeRe.FindStringSubmatch(line); len(match) >= 2 {
			inCue = true
			cueStart = match[1]
			continue
		}

		// Empty line ends a cue
		if strings.TrimSpace(line) == "" {
			inCue = false
			continue
		}

		// Skip cue identifiers (numeric or alphanumeric IDs before timestamps)
		if !inCue {
			continue
		}

		// This is cue text
		text := strings.TrimSpace(line)
		// Remove HTML tags like <v Speaker>
		text = regexp.MustCompile(`<[^>]+>`).ReplaceAllString(text, "")
		// Clean up speaker markers like @:@User1@:@ -> Lecturer:
		text = cleanSpeakerMarkers(text, "", nil)
		text = strings.TrimSpace(text)

		if text == "" || text == lastText {
			continue
		}

		if !wroteHeader {
			fmt.Fprintf(out, "TRANSCRIPT\n")
			fmt.Fprintf(out, "==========\n\n")
			wroteHeader = true
		}

		fmt.Fprintf(out, "[%s] %s\n", cueStart, text)
		lastText = text
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	return nil
}

// cleanSpeakerMarkers replaces Adobe Connect speaker markers with real names or readable labels.
// @:@User1@:@ -> actual name from mapping, or lecturerName, or "Lecturer:"
// @:@UserN@:@ -> actual name from mapping, or "Speaker N:"
// Also removes any remaining @:@ markers.
func cleanSpeakerMarkers(text, lecturerName string, userMapping map[string]string) string {
	// Replace @:@UserN@:@ with real names or fallback labels
	text = regexp.MustCompile(`@:@User(\d+)@:@`).ReplaceAllStringFunc(text, func(match string) string {
		re := regexp.MustCompile(`@:@User(\d+)@:@`)
		m := re.FindStringSubmatch(match)
		if len(m) >= 2 {
			userNum := m[1]
			userKey := "User" + userNum

			// Try to use real name from mapping
			if userMapping != nil {
				if realName, ok := userMapping[userKey]; ok && realName != "" {
					return realName + ":"
				}
			}

			// Fallback for User1 (lecturer)
			if userNum == "1" {
				if lecturerName != "" {
					return lecturerName + ":"
				}
				return "Lecturer:"
			}

			// Fallback for other users
			return fmt.Sprintf("Speaker %s:", userNum)
		}
		return match
	})

	// Remove any remaining @:@ markers
	text = strings.ReplaceAll(text, "@:@", "")
	return text
}

// cleanVTTFile creates a cleaned copy of a VTT file with speaker markers replaced by real names.
func cleanVTTFile(srcPath, dstPath, lecturerName string, userMapping map[string]string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	defer dst.Close()

	scanner := bufio.NewScanner(src)
	for scanner.Scan() {
		line := scanner.Text()
		// Clean speaker markers in cue text lines
		cleaned := cleanSpeakerMarkers(line, lecturerName, userMapping)
		fmt.Fprintln(dst, cleaned)
	}

	return scanner.Err()
}

// FindVTTPath looks for the VTT file in known locations within a result.
func FindVTTPath(result Result) string {
	possiblePaths := []string{
		filepath.Join(result.RootDir, "captions.vtt"),
	}
	if result.ExtractedDir != "" {
		possiblePaths = append(possiblePaths, filepath.Join(result.ExtractedDir, "captions.vtt"))
	}
	for _, p := range possiblePaths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

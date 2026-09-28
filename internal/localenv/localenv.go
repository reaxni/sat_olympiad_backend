package localenv

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var namePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Load reads an optional local .env file. Railway and production use only their
// injected process environment. Existing process variables always take priority.
func Load(path string) error {
	if os.Getenv("APP_ENV") == "production" || os.Getenv("RAILWAY_ENVIRONMENT_ID") != "" {
		return nil
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open local environment file: %w", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "\ufeff"))
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		text = strings.TrimPrefix(text, "export ")
		name, value, ok := strings.Cut(text, "=")
		name = strings.TrimSpace(name)
		if !ok || !namePattern.MatchString(name) {
			return fmt.Errorf("invalid .env assignment at line %d", line)
		}
		if _, exists := os.LookupEnv(name); exists {
			continue
		}
		value = strings.TrimSpace(value)
		if strings.HasPrefix(value, "\"") {
			value, err = strconv.Unquote(value)
			if err != nil {
				return fmt.Errorf("invalid quoted .env value at line %d", line)
			}
		} else if strings.HasPrefix(value, "'") {
			if len(value) < 2 || !strings.HasSuffix(value, "'") {
				return fmt.Errorf("invalid quoted .env value at line %d", line)
			}
			value = value[1 : len(value)-1]
		}
		if err := os.Setenv(name, value); err != nil {
			return fmt.Errorf("set .env variable at line %d: %w", line, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read local environment file: %w", err)
	}
	return nil
}

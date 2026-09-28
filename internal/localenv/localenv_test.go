package localenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadsLocalFileWithoutOverridingProcess(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATABASE_URL", "process-value")
	t.Setenv("RAILWAY_ENVIRONMENT_ID", "")
	const key = "LOCALENV_TEST_SECRET"
	os.Unsetenv(key)
	t.Cleanup(func() { os.Unsetenv(key) })
	path := testFile(t, "# local only\nDATABASE_URL=file-value\n"+key+"=\"quoted value\"\n")
	if err := Load(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("DATABASE_URL"); got != "process-value" {
		t.Fatal("process value was overwritten")
	}
	if got := os.Getenv(key); got != "quoted value" {
		t.Fatalf("quoted local value = %q", got)
	}
}

func TestSkipsFileOnRailwayAndProduction(t *testing.T) {
	path := testFile(t, "invalid line without equals\n")
	t.Setenv("APP_ENV", "production")
	if err := Load(path); err != nil {
		t.Fatal("production should skip .env:", err)
	}
	t.Setenv("APP_ENV", "development")
	t.Setenv("RAILWAY_ENVIRONMENT_ID", "railway-test")
	if err := Load(path); err != nil {
		t.Fatal("Railway should skip .env:", err)
	}
}

func TestInvalidLineDoesNotExposeValues(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("RAILWAY_ENVIRONMENT_ID", "")
	secret := "private-value-not-for-errors"
	err := Load(testFile(t, "BROKEN \""+secret+"\"\n"))
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatal("expected a safe parsing error")
	}
}

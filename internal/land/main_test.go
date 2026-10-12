package land_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/brynbellomy/beads-tools/internal/land"
)

// driver runs this build of beads-tools as the merge driver.
var driver string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "beads-tools-bin-*")
	if err != nil {
		panic(err)
	}
	bin := filepath.Join(dir, "beads-tools")
	build := exec.Command("go", "build", "-o", bin, "github.com/brynbellomy/beads-tools/cmd/beads-tools")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		panic(err)
	}
	driver = land.DriverCommand(bin)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

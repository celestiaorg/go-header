package sync

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestForkFollowingPrevention tests that syncer will crash if it detects
// that it is following a fork. This test must be executed as a separate
// program given the need to capture the crash.
//
// 1. syncer gets a valid subjective head
// 2. triggers sync job
// 3. syncing from malicious "eclipsing" peers who are giving it a fork
// 4. sync up to subjective head - 1, try to apply subjective head (fails)
// 5. ensure fork is detected and Fatal is thrown
func TestForkFollowingPrevention(t *testing.T) {
	path, err := filepath.Abs("./fork_test/fork.go")
	require.NoError(t, err)

	cmd := exec.Command("go", "run", path)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	require.Error(t, err)
	require.Equal(t, 1, cmd.ProcessState.ExitCode())
}

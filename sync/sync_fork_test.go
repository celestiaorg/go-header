package sync

import (
	"github.com/stretchr/testify/require"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TODO
// 1. syncer gets a valid subjective head
// 2. triggers sync job
// 3. syncing from malicious peers who are giving it a fork
// 4. sync up to subjective head - 1, try to apply subjective head (fails)
// 5. ensure fork is tossed and Fatal is thrown
func TestForkFollowingPrevention(t *testing.T) {
	path, err := filepath.Abs("./fork_test/fork.go")
	require.NoError(t, err)

	cmd := exec.Command("go", "run", path)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	require.Error(t, err)
	require.Contains(t, err.Error(), "exit status 1")
}

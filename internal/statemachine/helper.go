package statemachine

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/snapcore/snapd/seed"
	"github.com/snapcore/snapd/timings"

	"operese/cedar/internal/helper"
)

var runCmd = helper.RunCmd
var blockSize string = "1"

var (
	MKE2FS_CONFIG_ENV  = "MKE2FS_CONFIG"
	MKE2FS_CONFIG_FILE = "mke2fs.conf"
	MKE2FS_BASE_PATH   = "/etc/cedar/mkfs"
)

// validateInput ensures that command line flags for the state machine are valid. These
// flags are applicable to all image types
func (stateMachine *StateMachine) validateInput() error {
	// Validate command line options
	if stateMachine.stateMachineFlags.Thru != "" && stateMachine.stateMachineFlags.Until != "" {
		return fmt.Errorf("cannot specify both --until and --thru")
	}
	if stateMachine.stateMachineFlags.WorkDir == "" && stateMachine.stateMachineFlags.Resume {
		return fmt.Errorf("must specify workdir when using --resume flag")
	}

	logLevelFlags := []bool{stateMachine.commonFlags.Debug,
		stateMachine.commonFlags.Verbose,
		stateMachine.commonFlags.Quiet,
	}

	logLevels := 0
	for _, logLevelFlag := range logLevelFlags {
		if logLevelFlag {
			logLevels++
		}
	}

	if logLevels > 1 {
		return fmt.Errorf("--quiet, --verbose, and --debug flags are mutually exclusive")
	}

	return nil
}

func (stateMachine *StateMachine) setConfDefDir(confFileArg string) error {
	path, err := filepath.Abs(filepath.Dir(confFileArg))
	if err != nil {
		return fmt.Errorf("unable to determine the configuration definition directory: %w", err)
	}
	stateMachine.ConfDefPath = path

	return nil
}

// validateUntilThru validates that the the state passed as --until
// or --thru exists in the state machine's list of states
func (stateMachine *StateMachine) validateUntilThru() error {
	// if --until or --thru was given, make sure the specified state exists
	var searchState string
	var stateFound bool = false
	if stateMachine.stateMachineFlags.Until != "" {
		searchState = stateMachine.stateMachineFlags.Until
	}
	if stateMachine.stateMachineFlags.Thru != "" {
		searchState = stateMachine.stateMachineFlags.Thru
	}

	if searchState != "" {
		for _, state := range stateMachine.states {
			if state.name == searchState {
				stateFound = true
				break
			}
		}
		if !stateFound {
			return fmt.Errorf("state %s is not a valid state name", searchState)
		}
	}

	return nil
}

// cleanup cleans the workdir. For now this is just deleting the temporary directory if necessary
// but will have more functionality added to it later
func (stateMachine *StateMachine) cleanup() error {
	if stateMachine.cleanWorkDir {
		if err := osRemoveAll(stateMachine.stateMachineFlags.WorkDir); err != nil {
			return fmt.Errorf("Error cleaning up workDir: %s", err.Error())
		}
	}
	return nil
}

// WriteSnapManifest generates a snap manifest based on the contents of the selected snapsDir
func WriteSnapManifest(snapsDir string, outputPath string) error {
	files, err := osReadDir(snapsDir)
	if err != nil {
		// As per previous cedar manifest generation, we skip generating
		// manifests for non-existent/invalid paths
		return nil
	}

	manifest, err := osCreate(outputPath)
	if err != nil {
		return fmt.Errorf("Error creating manifest file: %s", err.Error())
	}
	defer manifest.Close()

	for _, file := range files {
		if strings.HasSuffix(file.Name(), ".snap") {
			split := strings.SplitN(file.Name(), "_", 2)
			fmt.Fprintf(manifest, "%s %s\n", split[0], strings.TrimSuffix(split[1], ".snap"))
		}
	}
	return nil
}

// parseSnapsAndChannels converts the command line arguments to a format that is expected
// by snapd's image.Prepare()
func parseSnapsAndChannels(snaps []string) (snapNames []string, snapChannels map[string]string, err error) {
	snapNames = make([]string, len(snaps))
	snapChannels = make(map[string]string)
	for ii, snap := range snaps {
		if strings.Contains(snap, "=") {
			splitSnap := strings.Split(snap, "=")
			if len(splitSnap) != 2 {
				return snapNames, snapChannels,
					fmt.Errorf("Invalid syntax passed to --snap: %s. "+
						"Argument must be in the form --snap=name or "+
						"--snap=name=channel", snap)
			}
			snapNames[ii] = splitSnap[0]
			snapChannels[splitSnap[0]] = splitSnap[1]
		} else {
			snapNames[ii] = snap
		}
	}
	return snapNames, snapChannels, nil
}

// execTeardownCmds executes given commands and collects error to join them with an existing error.
// Failure to execute one command will not stop from executing following ones.
func execTeardownCmds(teardownCmds []*exec.Cmd, debug bool, prevErr error) (err error) {
	err = prevErr
	errs := make([]string, 0)
	for _, teardownCmd := range teardownCmds {
		cmdOutput := helper.SetCommandOutput(teardownCmd, debug)
		teardownErr := teardownCmd.Run()
		if teardownErr != nil {
			errs = append(errs, fmt.Sprintf("teardown command  \"%s\" failed. Output: \n%s",
				teardownCmd.String(), cmdOutput.String()))
		}
	}

	if len(errs) > 0 {
		err = fmt.Errorf("teardown failed: %s", strings.Join(errs, "\n"))
		if prevErr != nil {
			errs := append([]string{prevErr.Error()}, errs...)
			err = errors.New(strings.Join(errs, "\n"))
		}
	}

	return err
}

// getPreseedsnaps returns a slice of the snaps that were preseeded in a chroot
// and their channels
func getPreseededSnaps(rootfs string) (seededSnaps map[string]string, err error) {
	// seededSnaps maps the snap name and channel that was seeded
	seededSnaps = make(map[string]string)

	// open the seed and run LoadAssertions and LoadMeta to get a list of snaps
	snapdDir := filepath.Join(rootfs, "var", "lib", "snapd")
	seedDir := filepath.Join(snapdDir, "seed")
	preseed, err := seedOpen(seedDir, "")
	if err != nil {
		return seededSnaps, err
	}
	measurer := timings.New(nil)
	if err := preseed.LoadAssertions(nil, nil); err != nil {
		return seededSnaps, err
	}
	if err := preseed.LoadMeta(seed.AllModes, nil, measurer); err != nil {
		return seededSnaps, err
	}

	// iterate over the snaps in the seed and add them to the list
	err = preseed.Iter(func(sn *seed.Snap) error {
		seededSnaps[sn.SnapName()] = sn.Channel
		return nil
	})
	if err != nil {
		return seededSnaps, err
	}

	return seededSnaps, nil
}

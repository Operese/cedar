// Package statemachine provides the functions and structs to set up and
// execute a state machine based cedar build
package statemachine

import (
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	diskfs "github.com/diskfs/go-diskfs"
	"github.com/snapcore/snapd/gadget"
	"github.com/snapcore/snapd/gadget/quantity"
	"github.com/snapcore/snapd/image"
	"github.com/snapcore/snapd/osutil"
	"github.com/snapcore/snapd/osutil/mkfs"
	"github.com/snapcore/snapd/seed"
	"github.com/xeipuuv/gojsonschema"

	"operese/cedar/internal/commands"
	"operese/cedar/internal/helper"
)

const (
	metadataStateFile = "cedar.json"
)

var gadgetYamlPathInTree = filepath.Join("meta", "gadget.yaml")

// define some functions that can be mocked by test cases
var gadgetLayoutVolume = gadget.LayoutVolume
var gadgetNewMountedFilesystemWriter = gadget.NewMountedFilesystemWriter
var helperCopyBlob = helper.CopyBlob
var helperSetDefaults = helper.SetDefaults
var helperCheckEmptyFields = helper.CheckEmptyFields
var helperCheckTags = helper.CheckTags
var helperBackupAndCopyResolvConf = helper.BackupAndCopyResolvConf
var helperRestoreResolvConf = helper.RestoreResolvConf
var osReadDir = os.ReadDir
var osReadFile = os.ReadFile
var osWriteFile = os.WriteFile
var osMkdir = os.Mkdir
var osMkdirAll = os.MkdirAll
var osMkdirTemp = os.MkdirTemp
var osOpen = os.Open
var osOpenFile = os.OpenFile
var osRemoveAll = os.RemoveAll
var osRemove = os.Remove
var osRename = os.Rename
var osCreate = os.Create
var osTruncate = os.Truncate
var osGetenv = os.Getenv
var osSetenv = os.Setenv
var osutilCopyFile = osutil.CopyFile
var osutilCopySpecialFile = osutil.CopySpecialFile
var execCommand = exec.Command
var mkfsMakeWithContent = mkfs.MakeWithContent
var mkfsMake = mkfs.Make
var diskfsCreate = diskfs.Create
var randRead = rand.Read
var seedOpen = seed.Open
var imagePrepare = image.Prepare
var gojsonschemaValidate = gojsonschema.Validate
var filepathRel = filepath.Rel

// SmInterface allows different image types to implement their own setup/run/teardown functions
type SmInterface interface {
	Setup() error
	Run() error
	Teardown() error
	SetCommonOpts(commonOpts *commands.CommonOpts, stateMachineOpts *commands.StateMachineOpts)
	SetSeries() error
}

// stateFunc allows us easy access to the function names, which will help with --resume and debug statements
type stateFunc struct {
	name     string
	function func(*StateMachine) error
}

// StateMachine will hold the command line data, track the current state, and handle all function calls
type StateMachine struct {
	CurrentStep   string        // tracks the current progress of the state machine
	StepsTaken    int           // counts the number of steps taken
	YamlFilePath  string        // the location for the gadget yaml file
	IsSeeded      bool          // core 20 images are seeded
	RootfsVolName string        // volume on which the rootfs is located
	RootfsPartNum int           // rootfs partition number
	SectorSize    quantity.Size // parsed (converted) sector size
	RootfsSize    quantity.Size

	series string

	// The flags that were passed in on the command line
	commonFlags       *commands.CommonOpts
	stateMachineFlags *commands.StateMachineOpts

	states []stateFunc // the state functions

	// used to access image type specific variables from state functions
	parent SmInterface

	// imported from snapd, the info parsed from gadget.yaml
	GadgetInfo *gadget.Info

	// Initially filled with the parsing of --image-size flags
	// Will then track the required size
	ImageSizes  map[string]quantity.Size
	VolumeOrder []string

	// names of images for each volume
	VolumeNames map[string]string

	// name of the "main volume"
	MainVolumeName string

	Packages []string
	Snaps    []string
}

// SetCommonOpts stores the common options for all image types in the struct
func (stateMachine *StateMachine) SetCommonOpts(commonOpts *commands.CommonOpts,
	stateMachineOpts *commands.StateMachineOpts) {
	stateMachine.commonFlags = commonOpts
	stateMachine.stateMachineFlags = stateMachineOpts
}

// displayStates print the calculated states
func (s *StateMachine) displayStates() {
	if !s.commonFlags.Debug && !s.commonFlags.DryRun {
		return
	}

	verb := "will"
	if s.commonFlags.DryRun {
		verb = "would"
	}
	fmt.Printf("\nFollowing states %s be executed:\n", verb)

	for i, state := range s.states {
		if state.name == s.stateMachineFlags.Until {
			break
		}
		fmt.Printf("[%d] %s\n", i, state.name)

		if state.name == s.stateMachineFlags.Thru {
			break
		}
	}

	if s.commonFlags.DryRun {
		return
	}
	fmt.Println("\nContinuing")
}

// Run iterates through the state functions, stopping when appropriate based on --until and --thru
func (stateMachine *StateMachine) Run() error {
	if stateMachine.commonFlags.DryRun {
		return nil
	}
	// iterate through the states
	for i := 0; i < len(stateMachine.states); i++ {
		stateFunc := stateMachine.states[i]
		stateMachine.CurrentStep = stateFunc.name
		if stateFunc.name == stateMachine.stateMachineFlags.Until {
			break
		}
		if !stateMachine.commonFlags.Quiet {
			fmt.Printf("[%d] %s\n", stateMachine.StepsTaken, stateFunc.name)
		}
		start := time.Now()
		err := stateFunc.function(stateMachine)
		if stateMachine.commonFlags.Debug {
			fmt.Printf("duration: %v\n", time.Since(start))
		}
		if err != nil {
			return err
		}
		stateMachine.StepsTaken++
		if stateFunc.name == stateMachine.stateMachineFlags.Thru {
			break
		}
	}
	fmt.Println("Build successful")
	return nil
}

// Teardown handles anything else that needs to happen after the states have finished running
func (stateMachine *StateMachine) Teardown() error {
	return nil
}

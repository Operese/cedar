// Package statemachine provides the functions and structs to set up and
// execute a state machine based ubuntu-image build
package statemachine

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	diskfs "github.com/diskfs/go-diskfs"
	"github.com/google/uuid"
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
	metadataStateFile = "ubuntu-image.json"
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

// temporaryDirectories organizes the state machines, rootfs, unpack, and volumes dirs
type temporaryDirectories struct {
	rootfs  string // finale location of the built rootfs
	unpack  string // directory holding the unpacked gadget tree (and thus boot assets)
	volumes string // directory holding resulting partial images associated to volumes declared in the gadget.yaml
	chroot  string // place where the rootfs is built and modified
	scratch string // place to build and mount some directories at various stage
}

// StateMachine will hold the command line data, track the current state, and handle all function calls
type StateMachine struct {
	cleanWorkDir  bool          // whether or not to clean up the workDir
	CurrentStep   string        // tracks the current progress of the state machine
	StepsTaken    int           // counts the number of steps taken
	ConfDefPath   string        // directory holding the model assertion / image definition file
	YamlFilePath  string        // the location for the gadget yaml file
	IsSeeded      bool          // core 20 images are seeded
	RootfsVolName string        // volume on which the rootfs is located
	RootfsPartNum int           // rootfs partition number
	SectorSize    quantity.Size // parsed (converted) sector size
	RootfsSize    quantity.Size
	tempDirs      temporaryDirectories

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

// readMetadata reads info about a partial state machine encoded as JSON from disk
func (stateMachine *StateMachine) readMetadata(metadataFile string) error {
	if !stateMachine.stateMachineFlags.Resume {
		return nil
	}
	// open the ubuntu-image.json file and load the state
	var partialStateMachine = &StateMachine{}
	jsonfilePath := filepath.Join(stateMachine.stateMachineFlags.WorkDir, metadataFile)
	jsonfile, err := os.ReadFile(jsonfilePath)
	if err != nil {
		return fmt.Errorf("error reading metadata file: %s", err.Error())
	}

	err = json.Unmarshal(jsonfile, partialStateMachine)
	if err != nil {
		return fmt.Errorf("failed to parse metadata file: %s", err.Error())
	}

	return stateMachine.loadState(partialStateMachine)
}

func (stateMachine *StateMachine) loadState(partialStateMachine *StateMachine) error {
	stateMachine.StepsTaken = partialStateMachine.StepsTaken

	if stateMachine.StepsTaken > len(stateMachine.states) {
		return fmt.Errorf("invalid steps taken count (%d). The state machine only have %d steps", stateMachine.StepsTaken, len(stateMachine.states))
	}

	// delete all of the stateFuncs that have already run
	stateMachine.states = stateMachine.states[stateMachine.StepsTaken:]

	stateMachine.CurrentStep = partialStateMachine.CurrentStep
	stateMachine.YamlFilePath = partialStateMachine.YamlFilePath
	stateMachine.IsSeeded = partialStateMachine.IsSeeded
	stateMachine.RootfsVolName = partialStateMachine.RootfsVolName
	stateMachine.RootfsPartNum = partialStateMachine.RootfsPartNum

	stateMachine.SectorSize = partialStateMachine.SectorSize
	stateMachine.RootfsSize = partialStateMachine.RootfsSize

	stateMachine.tempDirs.rootfs = filepath.Join(stateMachine.stateMachineFlags.WorkDir, "root")
	stateMachine.tempDirs.unpack = filepath.Join(stateMachine.stateMachineFlags.WorkDir, "unpack")
	stateMachine.tempDirs.volumes = filepath.Join(stateMachine.stateMachineFlags.WorkDir, "volumes")
	stateMachine.tempDirs.chroot = filepath.Join(stateMachine.stateMachineFlags.WorkDir, "chroot")
	stateMachine.tempDirs.scratch = filepath.Join(stateMachine.stateMachineFlags.WorkDir, "scratch")

	stateMachine.GadgetInfo = partialStateMachine.GadgetInfo
	stateMachine.ImageSizes = partialStateMachine.ImageSizes
	stateMachine.VolumeOrder = partialStateMachine.VolumeOrder
	stateMachine.VolumeNames = partialStateMachine.VolumeNames
	stateMachine.MainVolumeName = partialStateMachine.MainVolumeName

	stateMachine.Packages = partialStateMachine.Packages
	stateMachine.Snaps = partialStateMachine.Snaps

	if stateMachine.GadgetInfo != nil {
		// Due to https://github.com/golang/go/issues/10415 we need to set back the volume
		// structs we reset before encoding (see writeMetadata())
		gadget.SetEnclosingVolumeInStructs(stateMachine.GadgetInfo.Volumes)

		rebuildYamlIndex(stateMachine.GadgetInfo)
	}

	return nil
}

// rebuildYamlIndex reset the YamlIndex field in VolumeStructure
// This field is not serialized (for a good reason) so it is lost when saving the metadata
// We consider here the JSON serialization keeps the struct order and we can naively
// consider the YamlIndex value is the same as the index of the structure in the structure slice.
func rebuildYamlIndex(info *gadget.Info) {
	for _, v := range info.Volumes {
		for i, s := range v.Structure {
			s.YamlIndex = i
			v.Structure[i] = s
		}
	}
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

// writeMetadata writes the state machine info to disk, encoded as JSON. This will be used when resuming a
// partial state machine run
func (stateMachine *StateMachine) writeMetadata(metadataFile string) error {
	jsonfilePath := filepath.Join(stateMachine.stateMachineFlags.WorkDir, metadataFile)
	jsonfile, err := os.OpenFile(jsonfilePath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil && !os.IsExist(err) {
		return fmt.Errorf("error opening JSON metadata file for writing: %s", jsonfilePath)
	}
	defer jsonfile.Close()

	b, err := json.Marshal(stateMachine)
	if err != nil {
		return fmt.Errorf("failed to JSON encode metadata: %w", err)
	}

	_, err = jsonfile.Write(b)
	if err != nil {
		return fmt.Errorf("failed to write metadata to file: %w", err)
	}
	return nil
}

// generate work directory file structure
func (stateMachine *StateMachine) makeTemporaryDirectories() error {
	// if no workdir was specified, open a /tmp dir
	if stateMachine.stateMachineFlags.WorkDir == "" {
		stateMachine.stateMachineFlags.WorkDir = filepath.Join("/tmp", "ubuntu-image-"+uuid.NewString())
		if err := osMkdir(stateMachine.stateMachineFlags.WorkDir, 0755); err != nil {
			return fmt.Errorf("Failed to create temporary directory: %s", err.Error())
		}
		stateMachine.cleanWorkDir = true
	} else {
		err := osMkdirAll(stateMachine.stateMachineFlags.WorkDir, 0755)
		if err != nil && !os.IsExist(err) {
			return fmt.Errorf("Error creating work directory: %s", err.Error())
		}
	}

	stateMachine.tempDirs.rootfs = filepath.Join(stateMachine.stateMachineFlags.WorkDir, "root")
	stateMachine.tempDirs.unpack = filepath.Join(stateMachine.stateMachineFlags.WorkDir, "unpack")
	stateMachine.tempDirs.volumes = filepath.Join(stateMachine.stateMachineFlags.WorkDir, "volumes")
	stateMachine.tempDirs.chroot = filepath.Join(stateMachine.stateMachineFlags.WorkDir, "chroot")
	stateMachine.tempDirs.scratch = filepath.Join(stateMachine.stateMachineFlags.WorkDir, "scratch")

	tempDirs := []string{stateMachine.tempDirs.scratch, stateMachine.tempDirs.rootfs, stateMachine.tempDirs.unpack}
	for _, tempDir := range tempDirs {
		err := osMkdir(tempDir, 0755)
		if err != nil && !os.IsExist(err) {
			return fmt.Errorf("Error creating temporary directory \"%s\": \"%s\"", tempDir, err.Error())
		}
	}

	return nil
}

// determineOutputDirectory sets the directory in which to place artifacts
// and creates it if it doesn't already exist
func (stateMachine *StateMachine) determineOutputDirectory() error {
	if stateMachine.commonFlags.OutputDir == "" {
		if stateMachine.cleanWorkDir { // no workdir specified, so create the image in the pwd
			var err error
			stateMachine.commonFlags.OutputDir, err = os.Getwd()
			if err != nil {
				return fmt.Errorf("Error creating OutputDir: %s", err.Error())
			}
		} else {
			stateMachine.commonFlags.OutputDir = stateMachine.stateMachineFlags.WorkDir
		}
	} else {
		err := osMkdirAll(stateMachine.commonFlags.OutputDir, 0755)
		if err != nil && !os.IsExist(err) {
			return fmt.Errorf("Error creating OutputDir: %s", err.Error())
		}
	}
	return nil
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
			// clean up work dir on error
			cleanupErr := stateMachine.cleanup()
			if cleanupErr != nil {
				return fmt.Errorf("error during cleanup: %s while cleaning after stateFunc error: %w", cleanupErr.Error(), err)
			}
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
	if stateMachine.commonFlags.DryRun {
		return nil
	}
	if stateMachine.cleanWorkDir {
		return stateMachine.cleanup()
	}
	return stateMachine.writeMetadata(metadataStateFile)
}

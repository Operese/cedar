// Package cedar provides a tool used for generating bootable images.
// You can use it to build two types of Ubuntu images:
//
// * Snap-based Ubuntu Core images from model assertions
// * Classical preinstalled Ubuntu images using image definitions
//
// cedar is intended to be used as Snap package available from
// https://snapcraft.io/cedar
//
// See the project README for more details:
// https://operese/cedar
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/jessevdk/go-flags"

	"operese/cedar/internal/commands"
	"operese/cedar/internal/helper"
	"operese/cedar/internal/statemachine"
)

// Version holds the cedar version number
// this is usually overridden at build time
var Version string = ""

// helper variables for unit testing
var osExit = os.Exit
var captureStd = helper.CaptureStd

var stateMachineLongDesc = `Options for controlling the internal state machine.
Other than -w, these options are mutually exclusive. When -u or -t is given,
the state machine can be resumed later with -r, but -w must be given in that
case since the state is saved in a cedar.json file in the working directory.`

func initStateMachine(commonOpts *commands.CommonOpts, stateMachineOpts *commands.StateMachineOpts, classicCommand *commands.ClassicCommand, cedarOpts *commands.ClassicOpts) (statemachine.SmInterface, error) {
	var stateMachine statemachine.SmInterface
	stateMachine = &statemachine.ClassicStateMachine{
		Args:    classicCommand.ClassicArgsPassed,
		Preseed: cedarOpts.Preseed,
	}

	stateMachine.SetCommonOpts(commonOpts, stateMachineOpts)

	return stateMachine, nil
}

func executeStateMachine(sm statemachine.SmInterface) error {
	if err := sm.Setup(); err != nil {
		return err
	}

	if err := sm.Run(); err != nil {
		return err
	}

	if err := sm.Teardown(); err != nil {
		return err
	}

	return nil
}

// parseFlags parses received flags and returns error code accordingly
func parseFlags(parser *flags.Parser, restoreStdout, restoreStderr func(), stdout, stderr io.Reader, version bool) (error, int) {
	if _, err := parser.Parse(); err != nil {
		if e, ok := err.(*flags.Error); ok {
			switch e.Type {
			case flags.ErrHelp:
				restoreStdout()
				restoreStderr()
				readStdout, err := io.ReadAll(stdout)
				if err != nil {
					fmt.Printf("Error reading from stdout: %s\n", err.Error())
					return err, 1
				}
				fmt.Println(string(readStdout))
				return e, 0
			case flags.ErrCommandRequired:
				if !version {
					restoreStdout()
					restoreStderr()
					readStderr, err := io.ReadAll(stderr)
					if err != nil {
						fmt.Printf("Error reading from stderr: %s\n", err.Error())
						return err, 1
					}
					fmt.Printf("Error: %s\n", string(readStderr))
					return e, 1
				}
			default:
				restoreStdout()
				restoreStderr()
				fmt.Printf("Error: %s\n", err.Error())
				return e, 1
			}
		}
	}
	return nil, 0
}

func main() { //nolint: gocyclo
	commonOpts := new(commands.CommonOpts)
	stateMachineOpts := new(commands.StateMachineOpts)
	cedarOpts := new(commands.ClassicOpts)
	classicCommand := new(commands.ClassicCommand)

	// set up the go-flags parser for command line options
	parser := flags.NewParser(classicCommand, flags.Default)
	_, err := parser.AddGroup("State Machine Options", stateMachineLongDesc, stateMachineOpts)
	if err != nil {
		fmt.Printf("Error: %s\n", err.Error())
		osExit(1)
		return
	}
	_, err = parser.AddGroup("Common Options", "Options common to every command", commonOpts)
	if err != nil {
		fmt.Printf("Error: %s\n", err.Error())
		osExit(1)
		return
	}

	_, err = parser.AddGroup("Cedar Options", "Options determining which Cedar stages will be executed.", cedarOpts)
	if err != nil {
		fmt.Printf("Error: %s\n", err.Error())
		osExit(1)
		return
	}

	// go-flags can be overzealous about printing errors that aren't actually errors
	// so we capture stdout/stderr while parsing and later decide whether to print
	stdout, restoreStdout, err := captureStd(&os.Stdout)
	if err != nil {
		fmt.Printf("Failed to capture stdout: %s\n", err.Error())
		osExit(1)
		return
	}
	defer restoreStdout()

	stderr, restoreStderr, err := captureStd(&os.Stderr)
	if err != nil {
		fmt.Printf("Failed to capture stderr: %s\n", err.Error())
		osExit(1)
		return
	}
	defer restoreStderr()

	// Parse the options provided and handle specific errors
	err, code := parseFlags(parser, restoreStdout, restoreStderr, stdout, stderr, commonOpts.Version)
	if err != nil {
		osExit(code)
		return
	}

	// restore stdout
	restoreStdout()
	restoreStderr()

	// in case user only requested version number, print and exit
	if commonOpts.Version {
		// we expect Version to be supplied at build time or fetched from the snap environment
		if Version == "" {
			Version = os.Getenv("SNAP_VERSION")
		}
		fmt.Printf("cedar %s\n", Version)
		osExit(0)
		return
	}

	// init the state machine
	sm, err := initStateMachine(commonOpts, stateMachineOpts, classicCommand, cedarOpts)
	if err != nil {
		fmt.Printf("Error: %s\n", err.Error())
		osExit(1)
		return
	}

	// let the state machine handle the image build
	err = executeStateMachine(sm)
	if err != nil {
		fmt.Printf("Error: %s\n", err.Error())
		osExit(1)
		return
	}
}

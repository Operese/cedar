// Package commands defines structures and variables used to
// parse command line input
package commands

// CommonOpts stores the options that are common to all image types
type CommonOpts struct {
	Debug      bool   `long:"debug" description:"Enable debugging output"`
	Verbose    bool   `short:"v" long:"verbose" description:"Enable verbose output"`
	Quiet      bool   `short:"q" long:"quiet" description:"Turn off all output"`
	Version    bool   `long:"version" description:"Print the version number of cedar and exit"`
	Channel    string `short:"c" long:"channel" description:"The default snap channel to use" value-name:"CHANNEL"`
	Validation string `long:"validation" description:"Control whether validations should be ignored or enforced" choice:"ignore" choice:"enforce"` //nolint:staticcheck,SA5008
	// The library we use to handle command-line flags (github.com/jessevdk/go-flags) relies on this method to list valid values for a flag, even though this is not a recommended way.
	// Ignore these warnings until we use another library.
	DryRun bool `long:"dry-run" description:"Print the states to be executed to build the image and return."`
}

// StateMachineOpts stores the options that are related to the state machine
type StateMachineOpts struct {
	Until string `short:"u" long:"until" description:"Run the state machine until the given STEP, non-inclusively. STEP must be the name of the step." value-name:"STEP" default:""`
	Thru  string `short:"t" long:"thru" description:"Run the state machine through the given STEP, inclusively. STEP must be the name of the step." value-name:"STEP" default:""`
}

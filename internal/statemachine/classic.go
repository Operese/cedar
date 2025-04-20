package statemachine

import (
	"fmt"
	"os"

	"github.com/invopop/jsonschema"
	"github.com/xeipuuv/gojsonschema"
	"gopkg.in/yaml.v2"

	"operese/cedar/internal/commands"
	"operese/cedar/internal/snaplist"
)

// ClassicStateMachine embeds StateMachine and adds the command line flags specific to classic images
type ClassicStateMachine struct {
	StateMachine
	ImageDef snaplist.SnapList
	Args     commands.ClassicArgs
	Preseed  bool
}

// Setup assigns variables and calls other functions that must be executed before Run()
func (classicStateMachine *ClassicStateMachine) Setup() error {
	// set the parent pointer of the embedded struct
	classicStateMachine.parent = classicStateMachine

	classicStateMachine.states = make([]stateFunc, 0)

	// do the validation common to all image types
	if err := classicStateMachine.validateInput(); err != nil {
		return err
	}

	if err := classicStateMachine.parseSnapList(); err != nil {
		return err
	}

	if err := classicStateMachine.calculateStates(); err != nil {
		return err
	}

	// validate values of until and thru
	if err := classicStateMachine.validateUntilThru(); err != nil {
		return err
	}

	if err := classicStateMachine.SetSeries(); err != nil {
		return err
	}

	classicStateMachine.displayStates()

	if classicStateMachine.commonFlags.DryRun {
		return nil
	}
	return nil
}

func (classicStateMachine *ClassicStateMachine) SetSeries() error {
	classicStateMachine.series = classicStateMachine.ImageDef.Series
	return nil
}

// parseSnapList parses the provided yaml file and ensures it is valid
func (stateMachine *StateMachine) parseSnapList() error {
	classicStateMachine := stateMachine.parent.(*ClassicStateMachine)

	snapList, err := readSnapList(classicStateMachine.Args.SnapList)
	if err != nil {
		return err
	}

	// populate the default values for snapList if they were not provided in
	// the image definition YAML file
	if err := helperSetDefaults(snapList); err != nil {
		return err
	}

	err = validateSnapList(snapList)
	if err != nil {
		return err
	}

	classicStateMachine.ImageDef = *snapList

	return nil
}

func readSnapList(snapListPath string) (*snaplist.SnapList, error) {
	snapList := &snaplist.SnapList{}
	imageFile, err := os.Open(snapListPath)
	if err != nil {
		return nil, fmt.Errorf("Error opening snap list file: %s", err.Error())
	}
	defer imageFile.Close()
	if err := yaml.NewDecoder(imageFile).Decode(snapList); err != nil {
		return nil, err
	}

	return snapList, nil
}

// validateSnapList validates the given snapList
// The official standard for YAML schemas states that they are an extension of
// JSON schema draft 4. We therefore validate the decoded YAML against a JSON
// schema. The workflow is as follows:
// 1. Use the jsonschema library to generate a schema from the struct definition
// 2. Load the created schema and parsed yaml into types defined by gojsonschema
// 3. Use the gojsonschema library to validate the parsed YAML against the schema
func validateSnapList(snapList *snaplist.SnapList) error {
	var jsonReflector jsonschema.Reflector

	// 1. parse the SnapList struct into a schema using the jsonschema tags
	schema := jsonReflector.Reflect(snaplist.SnapList{})

	// 2. load the schema and parsed YAML data into types understood by gojsonschema
	schemaLoader := gojsonschema.NewGoLoader(schema)
	snapListLoader := gojsonschema.NewGoLoader(snapList)

	// 3. validate the parsed data against the schema
	result, err := gojsonschemaValidate(schemaLoader, snapListLoader)
	if err != nil {
		return fmt.Errorf("Schema validation returned an error: %s", err.Error())
	}

	// TODO: I've created a PR upstream in xeipuuv/gojsonschema
	// https://github.com/xeipuuv/gojsonschema/pull/352
	// if it gets merged this can be removed
	err = helperCheckEmptyFields(snapList, result, schema)
	if err != nil {
		return err
	}

	if !result.Valid() {
		return fmt.Errorf("Schema validation failed: %s", result.Errors())
	}

	return nil
}

// calculateStates dynamically calculates all the states
// needed to build the image, as defined by the image-definition file
// that was loaded previously.
// If a new possible state is added to the classic build state machine, it
// should be added here (usually basing on contents of the image definition)
func (s *StateMachine) calculateStates() error {
	classicStateMachine := s.parent.(*ClassicStateMachine)

	s.states = append(s.states, prepareClassicImageState)
	if classicStateMachine.Preseed {
		s.states = append(s.states, preseedClassicImageState)
	}

	return nil
}

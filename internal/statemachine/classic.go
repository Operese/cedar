package statemachine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/invopop/jsonschema"
	"github.com/xeipuuv/gojsonschema"
	"gopkg.in/yaml.v2"

	"operese/cedar/internal/commands"
	"operese/cedar/internal/imagedefinition"
)

var snapSeedStates = []stateFunc{
	prepareClassicImageState,
	preseedClassicImageState,
}

// ClassicStateMachine embeds StateMachine and adds the command line flags specific to classic images
type ClassicStateMachine struct {
	StateMachine
	ImageDef imagedefinition.ImageDefinition
	Args     commands.ClassicArgs
}

// Setup assigns variables and calls other functions that must be executed before Run()
func (classicStateMachine *ClassicStateMachine) Setup() error {
	// set the parent pointer of the embedded struct
	classicStateMachine.parent = classicStateMachine

	classicStateMachine.states = make([]stateFunc, 0)

	if err := classicStateMachine.setConfDefDir(classicStateMachine.parent.(*ClassicStateMachine).Args.ImageDefinition); err != nil {
		return err
	}

	// do the validation common to all image types
	if err := classicStateMachine.validateInput(); err != nil {
		return err
	}

	if err := classicStateMachine.parseImageDefinition(); err != nil {
		return err
	}

	if err := classicStateMachine.calculateStates(); err != nil {
		return err
	}

	// validate values of until and thru
	if err := classicStateMachine.validateUntilThru(); err != nil {
		return err
	}

	// if --resume was passed, figure out where to start
	if err := classicStateMachine.readMetadata(metadataStateFile); err != nil {
		return err
	}

	if err := classicStateMachine.SetSeries(); err != nil {
		return err
	}

	classicStateMachine.displayStates()

	if classicStateMachine.commonFlags.DryRun {
		return nil
	}

	if err := classicStateMachine.makeTemporaryDirectories(); err != nil {
		return err
	}

	return classicStateMachine.determineOutputDirectory()
}

func (classicStateMachine *ClassicStateMachine) SetSeries() error {
	classicStateMachine.series = classicStateMachine.ImageDef.Series
	return nil
}

// parseImageDefinition parses the provided yaml file and ensures it is valid
func (stateMachine *StateMachine) parseImageDefinition() error {
	classicStateMachine := stateMachine.parent.(*ClassicStateMachine)

	imageDefinition, err := readImageDefinition(classicStateMachine.Args.ImageDefinition)
	if err != nil {
		return err
	}

	if imageDefinition.Rootfs != nil && imageDefinition.Rootfs.SourcesListDeb822 == nil {
		fmt.Print("WARNING: rootfs.sources-list-deb822 was not set. Please explicitly set the format desired for sources list in your image definition.\n")
	}

	// populate the default values for imageDefinition if they were not provided in
	// the image definition YAML file
	if err := helperSetDefaults(imageDefinition); err != nil {
		return err
	}

	if imageDefinition.Rootfs != nil && *imageDefinition.Rootfs.SourcesListDeb822 {
		fmt.Print("WARNING: rootfs.sources-list-deb822 is set to true. The DEB822 format will be used to manage sources list. Please make sure you are not building an image older than noble.\n")
	} else {
		fmt.Print("WARNING: rootfs.sources-list-deb822 is set to false. The deprecated format will be used to manage sources list. Please if possible adopt the new format.\n")
	}

	err = validateImageDefinition(imageDefinition)
	if err != nil {
		return err
	}

	classicStateMachine.ImageDef = *imageDefinition

	return nil
}

func readImageDefinition(imageDefPath string) (*imagedefinition.ImageDefinition, error) {
	imageDefinition := &imagedefinition.ImageDefinition{}
	imageFile, err := os.Open(imageDefPath)
	if err != nil {
		return nil, fmt.Errorf("Error opening image definition file: %s", err.Error())
	}
	defer imageFile.Close()
	if err := yaml.NewDecoder(imageFile).Decode(imageDefinition); err != nil {
		return nil, err
	}

	return imageDefinition, nil
}

// validateImageDefinition validates the given imageDefinition
// The official standard for YAML schemas states that they are an extension of
// JSON schema draft 4. We therefore validate the decoded YAML against a JSON
// schema. The workflow is as follows:
// 1. Use the jsonschema library to generate a schema from the struct definition
// 2. Load the created schema and parsed yaml into types defined by gojsonschema
// 3. Use the gojsonschema library to validate the parsed YAML against the schema
func validateImageDefinition(imageDefinition *imagedefinition.ImageDefinition) error {
	var jsonReflector jsonschema.Reflector

	// 1. parse the ImageDefinition struct into a schema using the jsonschema tags
	schema := jsonReflector.Reflect(imagedefinition.ImageDefinition{})

	// 2. load the schema and parsed YAML data into types understood by gojsonschema
	schemaLoader := gojsonschema.NewGoLoader(schema)
	imageDefinitionLoader := gojsonschema.NewGoLoader(imageDefinition)

	// 3. validate the parsed data against the schema
	result, err := gojsonschemaValidate(schemaLoader, imageDefinitionLoader)
	if err != nil {
		return fmt.Errorf("Schema validation returned an error: %s", err.Error())
	}

	err = validateGadget(imageDefinition, result)
	if err != nil {
		return err
	}

	err = validateCustomization(imageDefinition, result)
	if err != nil {
		return err
	}

	// TODO: I've created a PR upstream in xeipuuv/gojsonschema
	// https://github.com/xeipuuv/gojsonschema/pull/352
	// if it gets merged this can be removed
	err = helperCheckEmptyFields(imageDefinition, result, schema)
	if err != nil {
		return err
	}

	if !result.Valid() {
		return fmt.Errorf("Schema validation failed: %s", result.Errors())
	}

	return nil
}

// validateGadget validates the Gadget section of the image definition
func validateGadget(imageDefinition *imagedefinition.ImageDefinition, result *gojsonschema.Result) error {
	// Do custom validation for gadgetURL being required if gadget is not pre-built
	if imageDefinition.Gadget != nil {
		if imageDefinition.Gadget.GadgetType != "prebuilt" && imageDefinition.Gadget.GadgetURL == "" {
			jsonContext := gojsonschema.NewJsonContext("gadget_validation", nil)
			errDetail := gojsonschema.ErrorDetails{
				"key":   "gadget:type",
				"value": imageDefinition.Gadget.GadgetType,
			}
			result.AddError(
				imagedefinition.NewMissingURLError(
					gojsonschema.NewJsonContext("missingURL", jsonContext),
					52,
					errDetail,
				),
				errDetail,
			)
		}
	} else if imageDefinition.Artifacts != nil {
		diskUsed, err := helperCheckTags(imageDefinition.Artifacts, "is_disk")
		if err != nil {
			return fmt.Errorf("Error checking struct tags for Artifacts: \"%s\"", err.Error())
		}
		if diskUsed != "" {
			jsonContext := gojsonschema.NewJsonContext("image_without_gadget", nil)
			errDetail := gojsonschema.ErrorDetails{
				"key1": diskUsed,
				"key2": "gadget:",
			}
			result.AddError(
				imagedefinition.NewDependentKeyError(
					gojsonschema.NewJsonContext("dependentKey", jsonContext),
					52,
					errDetail,
				),
				errDetail,
			)
		}
	}

	return nil
}

// validateCustomization validates the Customization section of the image definition
func validateCustomization(imageDefinition *imagedefinition.ImageDefinition, result *gojsonschema.Result) error {
	if imageDefinition.Customization == nil {
		return nil
	}

	validateExtraPPAs(imageDefinition, result)
	if imageDefinition.Customization.Manual != nil {
		jsonContext := gojsonschema.NewJsonContext("manual_path_validation", nil)
		validateManualMakeDirs(imageDefinition, result, jsonContext)
		validateManualCopyFile(imageDefinition, result, jsonContext)
		validateManualTouchFile(imageDefinition, result, jsonContext)
	}

	return nil
}

// validateExtraPPAs validates the Customization.ExtraPPAs section of the image definition
func validateExtraPPAs(imageDefinition *imagedefinition.ImageDefinition, result *gojsonschema.Result) {
	for _, p := range imageDefinition.Customization.ExtraPPAs {
		if p.Auth != "" && p.Fingerprint == "" {
			jsonContext := gojsonschema.NewJsonContext("ppa_validation", nil)
			errDetail := gojsonschema.ErrorDetails{
				"ppaName": p.Name,
			}
			result.AddError(
				imagedefinition.NewInvalidPPAError(
					gojsonschema.NewJsonContext("missingPrivatePPAFingerprint",
						jsonContext),
					52,
					errDetail,
				),
				errDetail,
			)
		}
	}
}

// validateManualMakeDirs validates the Customization.Manual.MakeDirs section of the image definition
func validateManualMakeDirs(imageDefinition *imagedefinition.ImageDefinition, result *gojsonschema.Result, jsonContext *gojsonschema.JsonContext) {
	if imageDefinition.Customization.Manual.MakeDirs == nil {
		return
	}
	for _, mkdir := range imageDefinition.Customization.Manual.MakeDirs {
		validateAbsolutePath(mkdir.Path, "customization:manual:mkdir:destination", result, jsonContext)
	}
}

// validateManualCopyFile validates the Customization.Manual.CopyFile section of the image definition
func validateManualCopyFile(imageDefinition *imagedefinition.ImageDefinition, result *gojsonschema.Result, jsonContext *gojsonschema.JsonContext) {
	if imageDefinition.Customization.Manual.CopyFile == nil {
		return
	}
	for _, copy := range imageDefinition.Customization.Manual.CopyFile {
		validateAbsolutePath(copy.Dest, "customization:manual:copy-file:destination", result, jsonContext)
	}
}

// validateManualTouchFile validates the Customization.Manual.TouchFile section of the image definition
func validateManualTouchFile(imageDefinition *imagedefinition.ImageDefinition, result *gojsonschema.Result, jsonContext *gojsonschema.JsonContext) {
	if imageDefinition.Customization.Manual.TouchFile == nil {
		return
	}
	for _, touch := range imageDefinition.Customization.Manual.TouchFile {
		validateAbsolutePath(touch.TouchPath, "customization:manual:touch-file:path", result, jsonContext)
	}
}

// validateAbsolutePath validates the
func validateAbsolutePath(path string, errorKey string, result *gojsonschema.Result, jsonContext *gojsonschema.JsonContext) {
	// XXX: filepath.IsAbs() does returns true for paths like ../../../something
	// and those are NOT absolute paths.
	if !filepath.IsAbs(path) || strings.Contains(path, "/../") {
		errDetail := gojsonschema.ErrorDetails{
			"key":   errorKey,
			"value": path,
		}
		result.AddError(
			imagedefinition.NewPathNotAbsoluteError(
				gojsonschema.NewJsonContext("nonAbsoluteManualPath",
					jsonContext),
				52,
				errDetail,
			),
			errDetail,
		)
	}
}

// calculateStates dynamically calculates all the states
// needed to build the image, as defined by the image-definition file
// that was loaded previously.
// If a new possible state is added to the classic build state machine, it
// should be added here (usually basing on contents of the image definition)
func (s *StateMachine) calculateStates() error {
	s.states = append(s.states, snapSeedStates...)

	return nil
}

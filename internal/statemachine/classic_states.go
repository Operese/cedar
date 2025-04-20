package statemachine

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"

	"github.com/snapcore/snapd/image"
	"github.com/snapcore/snapd/image/preseed"
	"github.com/snapcore/snapd/interfaces/builtin"
	"github.com/snapcore/snapd/osutil"
	"github.com/snapcore/snapd/seed/seedwriter"
	"github.com/snapcore/snapd/snap"
	"github.com/snapcore/snapd/store"

	"operese/cedar/internal/helper"
	"operese/cedar/internal/snaplist"
)

var (
	seedVersionRegex   = regexp.MustCompile(`^[a-z0-9].*`)
	localePresentRegex = regexp.MustCompile(`(?m)^LANG=|LC_[A-Z_]+=`)
)

// addUniqueSnaps returns a list of unique snaps
func addUniqueSnaps(currentSnaps []string, newSnaps []string) []string {
	m := make(map[string]bool)
	snaps := []string{}
	toDuplicate := append(currentSnaps, newSnaps...)

	for _, s := range toDuplicate {
		if m[s] {
			continue
		}
		m[s] = true
		snaps = append(snaps, s)
	}
	return snaps
}

var prepareClassicImageState = stateFunc{"prepare_image", (*StateMachine).prepareClassicImage}

// prepareClassicImage calls image.Prepare to stage snaps in classic images
func (stateMachine *StateMachine) prepareClassicImage() error {
	classicStateMachine := stateMachine.parent.(*ClassicStateMachine)
	imageOpts := &image.Options{}
	var err error

	imageOpts.Snaps, imageOpts.SnapChannels, err = parseSnapsAndChannels(classicStateMachine.Snaps)
	if err != nil {
		return err
	}
	if stateMachine.commonFlags.Channel != "" {
		imageOpts.Channel = stateMachine.commonFlags.Channel
	}

	// plug/slot sanitization needed by provider handling
	snap.SanitizePlugsSlots = builtin.SanitizePlugsSlots

	err = resetPreseeding(imageOpts, classicStateMachine.Args.ImagePath, stateMachine.commonFlags.Debug, stateMachine.commonFlags.Verbose)
	if err != nil {
		return err
	}

	err = ensureSnapBasesInstalled(imageOpts)
	if err != nil {
		return err
	}

	err = addExtraSnaps(imageOpts, &classicStateMachine.ImageDef)
	if err != nil {
		return err
	}

	imageOpts.Classic = true
	imageOpts.Architecture = classicStateMachine.ImageDef.Architecture
	imageOpts.PrepareDir = classicStateMachine.Args.ImagePath
	imageOpts.Customizations = *new(image.Customizations)
	imageOpts.Customizations.Validation = stateMachine.commonFlags.Validation

	// image.Prepare automatically has some output that we only want for
	// verbose or greater logging
	if !stateMachine.commonFlags.Debug && !stateMachine.commonFlags.Verbose {
		oldImageStdout := image.Stdout
		image.Stdout = io.Discard
		defer func() {
			image.Stdout = oldImageStdout
		}()
	}

	if err := imagePrepare(imageOpts); err != nil {
		return fmt.Errorf("Error preparing image: %s", err.Error())
	}

	return nil
}

// resetPreseeding checks if the rootfs is already preseeded and reset if necessary.
// This can happen when building from a rootfs tarball
func resetPreseeding(imageOpts *image.Options, chroot string, debug, verbose bool) error {
	if !osutil.FileExists(filepath.Join(chroot, "var", "lib", "snapd", "state.json")) {
		return nil
	}
	// first get a list of all preseeded snaps
	// seededSnaps maps the snap name and channel that was seeded
	preseededSnaps, err := getPreseededSnaps(chroot)
	if err != nil {
		return fmt.Errorf("Error getting list of preseeded snaps from existing rootfs: %s",
			err.Error())
	}
	for snap, channel := range preseededSnaps {
		// if a channel is specified on the command line for a snap that was already
		// preseeded, use the channel from the command line instead of the channel
		// that was originally used for the preseeding
		if !helper.SliceHasElement(imageOpts.Snaps, snap) {
			imageOpts.Snaps = append(imageOpts.Snaps, snap)
			imageOpts.SnapChannels[snap] = channel
		}
	}
	// preseed.ClassicReset automatically has some output that we only want for
	// verbose or greater logging
	if !debug && !verbose {
		oldPreseedStdout := preseed.Stdout
		preseed.Stdout = io.Discard
		defer func() {
			preseed.Stdout = oldPreseedStdout
		}()
	}
	// We need to use the snap-preseed binary for the reset as well, as using
	// preseed.ClassicReset() might leave us in a chroot jail
	cmd := execCommand(fmt.Sprintf("%s/usr/lib/snapd/snap-preseed", chroot), "--reset", chroot)
	err = cmd.Run()
	if err != nil {
		return fmt.Errorf("Error resetting preseeding in the chroot. Error is \"%s\"", err.Error())
	}

	return nil
}

// ensureSnapBasesInstalled iterates through the list of snaps and ensure that all
// of their bases are also set to be installed. Note we only do this for snaps that
// are seeded. Users are expected to specify all base and content provider snaps
// in the image definition.
func ensureSnapBasesInstalled(imageOpts *image.Options) error {
	snapStore := store.New(nil, nil)
	snapContext := context.Background()
	for _, seededSnap := range imageOpts.Snaps {
		snapSpec := store.SnapSpec{Name: seededSnap}
		snapInfo, err := snapStore.SnapInfo(snapContext, snapSpec, nil)
		if err != nil {
			return fmt.Errorf("Error getting info for snap %s: \"%s\"",
				seededSnap, err.Error())
		}
		if snapInfo.Base != "" && !helper.SliceHasElement(imageOpts.Snaps, snapInfo.Base) {
			imageOpts.Snaps = append(imageOpts.Snaps, snapInfo.Base)
		}
	}
	return nil
}

// addExtraSnaps adds any extra snaps from the image definition to the list
// This should be done last to ensure the correct channels are being used
func addExtraSnaps(imageOpts *image.Options, snapList *snaplist.SnapList) error {
	imageOpts.SeedManifest = seedwriter.NewManifest()
	for _, extraSnap := range snapList.Snaps {
		if !helper.SliceHasElement(imageOpts.Snaps, extraSnap.SnapName) {
			imageOpts.Snaps = append(imageOpts.Snaps, extraSnap.SnapName)
		}
		if extraSnap.Channel != "" {
			imageOpts.SnapChannels[extraSnap.SnapName] = extraSnap.Channel
		}
		if extraSnap.SnapRevision != 0 {
			fmt.Printf("WARNING: revision %d for snap %s may not be the latest available version!\n",
				extraSnap.SnapRevision,
				extraSnap.SnapName,
			)
			err := imageOpts.SeedManifest.SetAllowedSnapRevision(extraSnap.SnapName, snap.R(extraSnap.SnapRevision))
			if err != nil {
				return fmt.Errorf("error dealing with the extra snap %s: %w", extraSnap.SnapName, err)
			}
		}
	}

	return nil
}

var preseedClassicImageState = stateFunc{"preseed_image", (*StateMachine).preseedClassicImage}

// preseedClassicImage preseeds the snaps that have already been staged in the chroot
func (stateMachine *StateMachine) preseedClassicImage() (err error) {
	classicStateMachine := stateMachine.parent.(*ClassicStateMachine)

	// preseedCmds should be filled as a FIFO list
	var preseedCmds []*exec.Cmd
	// teardownCmds should be filled as a LIFO list to unmount first what was mounted last
	var teardownCmds []*exec.Cmd

	// set up the mount commands
	mountPoints := []*mountPoint{
		{
			src:      "devtmpfs-build",
			basePath: classicStateMachine.Args.ImagePath,
			relpath:  "/dev",
			typ:      "devtmpfs",
		},
		{
			src:      "devpts-build",
			basePath: classicStateMachine.Args.ImagePath,
			relpath:  "/dev/pts",
			typ:      "devpts",
			opts:     []string{"nodev", "nosuid"},
		},
		{
			src:      "proc-build",
			basePath: classicStateMachine.Args.ImagePath,
			relpath:  "/proc",
			typ:      "proc",
		},
		{
			src:      "none",
			basePath: classicStateMachine.Args.ImagePath,
			relpath:  "/sys/kernel/security",
			typ:      "securityfs",
		},
		{
			src:      "none",
			basePath: classicStateMachine.Args.ImagePath,
			relpath:  "/sys/fs/cgroup",
			typ:      "cgroup2",
		},
	}

	// Make sure we left the system as clean as possible if something has gone wrong
	defer func() {
		err = teardownMount(classicStateMachine.Args.ImagePath, mountPoints, teardownCmds, err, stateMachine.commonFlags.Debug)
	}()

	for _, mp := range mountPoints {
		mountCmds, umountCmds, err := mp.getMountCmd()
		if err != nil {
			return fmt.Errorf("Error preparing mountpoint \"%s\": \"%s\"",
				mp.relpath,
				err.Error(),
			)
		}
		preseedCmds = append(preseedCmds, mountCmds...)
		teardownCmds = append(umountCmds, teardownCmds...)
	}

	teardownCmds = append([]*exec.Cmd{
		execCommand("udevadm", "settle"),
	}, teardownCmds...)

	preseedCmds = append(preseedCmds,
		//nolint:gosec,G204
		exec.Command(fmt.Sprintf("%s/usr/lib/snapd/snap-preseed", classicStateMachine.Args.ImagePath), classicStateMachine.Args.ImagePath),
	)

	err = helper.RunCmds(preseedCmds, classicStateMachine.commonFlags.Debug)
	if err != nil {
		return err
	}

	return nil
}

var setDefaultLocaleState = stateFunc{"set_default_locale", (*StateMachine).setDefaultLocale}

// Set a default locale if one is not configured beforehand by other customizations
func (stateMachine *StateMachine) setDefaultLocale() error {
	classicStateMachine := stateMachine.parent.(*ClassicStateMachine)

	defaultPath := filepath.Join(classicStateMachine.Args.ImagePath, "etc", "default")
	localePath := filepath.Join(defaultPath, "locale")
	localeBytes, err := osReadFile(localePath)
	if err == nil && localePresentRegex.Find(localeBytes) != nil {
		return nil
	}

	err = osMkdirAll(defaultPath, 0755)
	if err != nil {
		return fmt.Errorf("Error creating default directory: %s", err.Error())
	}

	err = osWriteFile(localePath, []byte("# Default Ubuntu locale\nLANG=C.UTF-8\n"), 0644)
	if err != nil {
		return fmt.Errorf("Error writing to locale file: %s", err.Error())
	}
	return nil
}

var cleanRootfsState = stateFunc{"clean_rootfs", (*StateMachine).cleanRootfs}

// cleanRootfs cleans the created chroot from secrets/values generated
// during the various preceding install steps
func (stateMachine *StateMachine) cleanRootfs() error {
	classicStateMachine := stateMachine.parent.(*ClassicStateMachine)

	toDelete := []string{
		filepath.Join(classicStateMachine.Args.ImagePath, "var", "lib", "dbus", "machine-id"),
	}

	toTruncate := []string{
		filepath.Join(classicStateMachine.Args.ImagePath, "etc", "machine-id"),
	}

	toCleanFromPattern, err := listWithPatterns(classicStateMachine.Args.ImagePath,
		[]string{
			filepath.Join("etc", "ssh", "ssh_host_*_key.pub"),
			filepath.Join("etc", "ssh", "ssh_host_*_key"),
			filepath.Join("var", "cache", "debconf", "*-old"),
			filepath.Join("var", "lib", "dpkg", "*-old"),
			filepath.Join("dev", "*"),
			filepath.Join("sys", "*"),
			filepath.Join("run", "*"),
		})
	if err != nil {
		return err
	}

	toDelete = append(toDelete, toCleanFromPattern...)

	err = doDeleteFiles(toDelete)
	if err != nil {
		return err
	}

	toTruncateFromPattern, err := listWithPatterns(classicStateMachine.Args.ImagePath,
		[]string{
			// udev persistent rules
			filepath.Join("etc", "udev", "rules.d", "*persistent-net.rules"),
		})
	if err != nil {
		return err
	}

	toTruncate = append(toTruncate, toTruncateFromPattern...)

	return doTruncateFiles(toTruncate)
}

func listWithPatterns(chroot string, patterns []string) ([]string, error) {
	files := make([]string, 0)
	for _, pattern := range patterns {
		matches, err := filepath.Glob(filepath.Join(chroot, pattern))
		if err != nil {
			return nil, fmt.Errorf("unable to list files for pattern %s: %s", pattern, err.Error())
		}

		files = append(files, matches...)
	}
	return files, nil
}

// doDeleteFiles deletes the given list of files
func doDeleteFiles(toDelete []string) error {
	for _, f := range toDelete {
		err := osRemoveAll(f)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("Error removing %s: %s", f, err.Error())
		}
	}
	return nil
}

// doTruncateFiles truncates content in the given list of files
func doTruncateFiles(toTruncate []string) error {
	for _, f := range toTruncate {
		err := osTruncate(f, 0)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("Error truncating %s: %s", f, err.Error())
		}
	}
	return nil
}

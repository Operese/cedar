package commands

// ClassicArgs holds the gadget tree. positional arguments need their own struct
type ClassicArgs struct {
	SnapList string `positional-arg-name:"snap_list" description:"Extra snap list file. This is used to define what snaps should be added to the image."`
}

type ClassicCommand struct {
	ClassicArgsPassed ClassicArgs `positional-args:"true" required:"false"`
}

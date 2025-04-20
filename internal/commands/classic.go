package commands

// ClassicArgs holds the gadget tree. positional arguments need their own struct
type ClassicArgs struct {
	ImagePath string `positional-arg-name:"image_path" description:"The path to the Ubuntu image where the snaps are to be preseeded. It could have been created with cedar or another tool."`
	SnapList  string `positional-arg-name:"snap_list"  description:"Extra snap list file. This is used to define what snaps should be added to the image."`
}

type ClassicOpts struct {
	Preseed bool `long:"preseed" required:"false" description:"Whether or not to run snap-preseed in the image to speed up first boot. Only works on hosts using AppArmor."`
}

type ClassicCommand struct {
	ClassicArgsPassed ClassicArgs `positional-args:"true" required:"false"`
}

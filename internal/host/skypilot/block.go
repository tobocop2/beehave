package skypilot

import (
	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/model"
)

// The keys of the skypilot block.
const (
	BlockCloud        = "cloud"
	BlockRegion       = "region"
	BlockInstanceType = "instance_type"
	// BlockInstanceSizes is the size table: rows of `<limit>:<type>` and a last row of a bare type, such as `3:n2-standard-4,n2-standard-16`.
	BlockInstanceSizes = "instance_sizes"
	// BlockDiskSizeGB is the size of the boot disk in GB. A project that leaves it out gets the disk size that SkyPilot picks.
	BlockDiskSizeGB = "disk_size_gb"
	// BlockSpot asks for a spot instance. The default is False.
	BlockSpot = "spot"
)

// Block returns the schema of the skypilot block: the machine that the sky command creates, every key optional.
func Block() config.Block {
	return config.Block{Name: "skypilot", Args: []config.Arg{
		{Key: BlockCloud, Optional: true},
		{Key: BlockRegion, Optional: true},
		{Key: BlockInstanceType, Optional: true},
		{Key: BlockInstanceSizes, Optional: true},
		{Key: BlockDiskSizeGB, Kind: config.WholeArg, Optional: true},
		{Key: BlockSpot, Kind: config.BoolArg, Optional: true},
	}}
}

// block is the skypilot block of the cluster, as the project file wrote it. A key that the file leaves out stays empty and leaves the choice to SkyPilot.
type block struct {
	cloud        string
	region       string
	instanceType string
	// instanceSizes is the size table as text. New parses it.
	instanceSizes string
	// diskSizeGB is the boot disk size. diskSized says the project wrote the key, so New can refuse a written zero.
	diskSizeGB int
	diskSized  bool
	spot       bool
}

// decodeBlock reads the skypilot block once. A nil block leaves every choice to SkyPilot. New checks the words and the numbers.
func decodeBlock(from *model.Block) block {
	var settings block

	settings.cloud, _ = from.Text(BlockCloud)
	settings.region, _ = from.Text(BlockRegion)
	settings.instanceType, _ = from.Text(BlockInstanceType)
	settings.instanceSizes, _ = from.Text(BlockInstanceSizes)
	settings.diskSizeGB, settings.diskSized = from.Whole(BlockDiskSizeGB)
	settings.spot, _ = from.Bool(BlockSpot)

	return settings
}

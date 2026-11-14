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
)

// Block returns the schema of the skypilot block: the machine that the sky command creates, every key optional.
func Block() config.Block {
	return config.Block{Name: "skypilot", Args: []config.Arg{
		{Key: BlockCloud, Optional: true}, {Key: BlockRegion, Optional: true}, {Key: BlockInstanceType, Optional: true},
	}}
}

// block is the skypilot block of the cluster, as the project file wrote it. A key that the file leaves out stays empty and leaves the choice to SkyPilot.
type block struct {
	cloud        string
	region       string
	instanceType string
}

// decodeBlock reads the skypilot block once. A nil block leaves every choice to SkyPilot. New checks the words.
func decodeBlock(from *model.Block) block {
	var settings block

	settings.cloud, _ = from.Text(BlockCloud)
	settings.region, _ = from.Text(BlockRegion)
	settings.instanceType, _ = from.Text(BlockInstanceType)

	return settings
}

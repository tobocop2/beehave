package skypilot

import (
	"regexp"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/model"
)

// Name is the name the SkyPilot host provider is chosen by.
const Name model.HostProvider = "skypilot"

// Block returns the schema of the skypilot block: the machine that the sky command creates, every key optional.
func Block() config.Block {
	return config.Block{Name: "skypilot", Args: []config.Arg{
		{Key: BlockCloud, Optional: true}, {Key: BlockRegion, Optional: true}, {Key: BlockInstanceType, Optional: true},
	}}
}

// The keys of the skypilot block.
const (
	BlockCloud        = "cloud"
	BlockRegion       = "region"
	BlockInstanceType = "instance_type"
)

var wordPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// block is the skypilot block of the cluster, as the project file wrote it.
type block struct {
	cloud        string
	region       string
	instanceType string
}

// decodeBlock reads the skypilot block. A nil block leaves every choice to SkyPilot.
func decodeBlock(from *model.Block) block {
	settings := block{}
	if from == nil {
		return settings
	}

	settings.cloud, _ = from.Text(BlockCloud)
	settings.region, _ = from.Text(BlockRegion)
	settings.instanceType, _ = from.Text(BlockInstanceType)

	return settings
}

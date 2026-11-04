package skypilot

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/tobocop2/obee/internal/model"
)

// The refusals of the parsers. Each wraps the text that was not recognized.
var (
	errClusterList = errors.New("sky status gave an answer that is not a list of clusters with a name and a status")
	errClusterUp   = errors.New("the cluster is not up")
)

const (
	clusterUp    = "UP"
	statusPrefix = "ClusterStatus."
)

// clusterRecord is one element of the answer of `sky status -o json`.
type clusterRecord struct {
	Name   *string `json:"name"`
	Status *string `json:"status"`
}

// parseCluster reads the answer of `sky status -r -o json`. It returns false when the cluster is not in the list, and an error when the answer is not a list of records or the cluster is not up.
func parseCluster(out, cluster string) (bool, error) {
	records, err := decodeClusters(out)
	if err != nil {
		return false, err
	}

	found := false

	for _, record := range records {
		if *record.Name != cluster {
			continue
		}

		found = true

		if status := strings.TrimPrefix(*record.Status, statusPrefix); status != clusterUp {
			return false, fmt.Errorf("%w: cluster %s is %q", errClusterUp, cluster, status)
		}
	}

	return found, nil
}

// decodeClusters reads the list. Every record must have a name and a status.
func decodeClusters(out string) ([]clusterRecord, error) {
	var records []clusterRecord

	if err := json.Unmarshal([]byte(out), &records); err != nil || records == nil {
		return nil, fmt.Errorf("%w: %q", errClusterList, model.FirstLine(out))
	}

	for _, record := range records {
		if record.Name == nil || record.Status == nil {
			return nil, fmt.Errorf("%w: %q", errClusterList, model.FirstLine(out))
		}
	}

	return records, nil
}

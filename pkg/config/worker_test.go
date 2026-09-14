package config

import (
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	"testing"
)

func TestLeaseRenewalConnectionLimit(t *testing.T) {
	for _, tc := range []struct {
		yaml    string
		want    int32
		invalid bool
	}{
		{"{}", 10, false},
		{"worker: {leaseRenewalMaxConnections: 1}", 1, false},
		{"worker: {leaseRenewalMaxConnections: 20}", 20, false},
		{"worker: {leaseRenewalMaxConnections: 0}", 0, true},
		{"worker: {leaseRenewalMaxConnections: -1}", 0, true},
	} {
		t.Run(tc.yaml, func(t *testing.T) {
			var cfg Config
			require.NoError(t, yaml.Unmarshal([]byte(tc.yaml), &cfg))
			got, err := cfg.Worker.LeaseRenewalConnectionLimit()
			if tc.invalid {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.want, got)
			}
		})
	}
}

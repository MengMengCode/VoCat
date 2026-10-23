package ike

import "testing"

func TestOpenWrtNFTCommandHonoursEnvironmentOverride(t *testing.T) {
	t.Parallel()
	environment := map[string]string{}
	getenv := func(key string) string { return environment[key] }

	if command := openWrtNFTCommand(getenv); command != "nft" {
		t.Fatalf("default nft command = %q", command)
	}
	environment[openWrtNFTCommandEnv] = "  /usr/local/sbin/host-nft  "
	if command := openWrtNFTCommand(getenv); command != "/usr/local/sbin/host-nft" {
		t.Fatalf("overridden nft command = %q", command)
	}
	environment[openWrtNFTCommandEnv] = "   "
	if command := openWrtNFTCommand(getenv); command != "nft" {
		t.Fatalf("blank override was not ignored: %q", command)
	}
}

func TestFW4InputChainAbsentOnlyForMissingFW4(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		exitCode int
		output   string
		absent   bool
	}{
		{
			name:     "missing table on a non-OpenWrt host",
			exitCode: 1,
			output:   "Error: No such file or directory\nlist chain inet fw4 input\n                   ^^^\n",
			absent:   true,
		},
		{
			name:     "kernel without nf_tables",
			exitCode: 1,
			output:   "netlink: Error: cache initialization failed: Protocol not supported\n",
			absent:   true,
		},
		{
			// Alpine's nft has been observed to segfault on some OpenWrt fw4
			// rulesets; the kernel reports that as exit code -1 (signal).
			name:     "nft crash",
			exitCode: -1,
			output:   "",
			absent:   false,
		},
		{
			name:     "permission denied",
			exitCode: 1,
			output:   "netlink: Error: cache initialization failed: Operation not permitted\n",
			absent:   false,
		},
	} {
		if absent := fw4InputChainAbsent(test.exitCode, test.output); absent != test.absent {
			t.Errorf("%s: fw4InputChainAbsent = %v, want %v", test.name, absent, test.absent)
		}
	}
}

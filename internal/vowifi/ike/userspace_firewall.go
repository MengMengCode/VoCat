package ike

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const openWrtFirewallCommentPrefix = "vocat-ims-"

type openWrtFirewallRule struct {
	comment   string
	arguments []string
}

// buildOpenWrtFirewallRules permits only P-CSCF traffic addressed to the
// negotiated inner address on the per-session TUN interface. OpenWrt fw4 uses
// a default-drop input chain for interfaces that are not managed by netifd;
// without these rules an IMS core can accept mobile-originated SMS while every
// network-originated SIP MESSAGE is rejected before it reaches VoCat.
func buildOpenWrtFirewallRules(config ChildSAConfig) []openWrtFirewallRule {
	name := strings.TrimSpace(config.Name)
	if name == "" {
		return nil
	}
	prefix := fmt.Sprintf("%s%08x-", openWrtFirewallCommentPrefix, config.InboundSPI)
	result := make([]openWrtFirewallRule, 0, len(config.PCSCF)*2)
	seen := make(map[string]struct{})
	for index, pcscf := range config.PCSCF {
		if pcscf == nil {
			continue
		}
		family := "ip"
		local := config.InnerLocalIPv4
		if pcscf.To4() == nil {
			family = "ip6"
			local = config.InnerLocalIPv6
		}
		if local == nil || local.IsUnspecified() {
			continue
		}
		source := pcscf.String()
		destination := local.String()
		for _, protocol := range []string{"tcp", "udp"} {
			key := strings.Join([]string{family, source, destination, protocol}, "|")
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			comment := fmt.Sprintf("%s%s-%d", prefix, protocol, index)
			result = append(result, openWrtFirewallRule{
				comment: comment,
				arguments: []string{
					"insert", "rule", "inet", "fw4", "input",
					"iifname", name,
					family, "saddr", source,
					family, "daddr", destination,
					"meta", "l4proto", protocol,
					"counter", "accept",
					"comment", comment,
				},
			})
		}
	}
	return result
}

var nftHandlePattern = regexp.MustCompile(`# handle ([0-9]+)\s*$`)

func nftRuleHandles(output string, comments map[string]struct{}) []string {
	var handles []string
	for _, line := range strings.Split(output, "\n") {
		matchedComment := false
		for comment := range comments {
			if strings.Contains(line, `comment "`+comment+`"`) {
				matchedComment = true
				break
			}
		}
		if !matchedComment {
			continue
		}
		match := nftHandlePattern.FindStringSubmatch(strings.TrimSpace(line))
		if len(match) == 2 {
			handles = append(handles, match[1])
		}
	}
	sort.Strings(handles)
	return handles
}

// staleOpenWrtFirewallRuleHandles returns managed rules for the same TUN
// interface that do not belong to the current Child SA. This removes rules
// left behind when a previous process was killed before graceful cleanup.
func staleOpenWrtFirewallRuleHandles(
	output string,
	interfaceName string,
	currentComments map[string]struct{},
) []string {
	interfaceName = strings.TrimSpace(interfaceName)
	if interfaceName == "" {
		return nil
	}
	interfaceMatch := `iifname "` + interfaceName + `"`
	var handles []string
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, interfaceMatch) ||
			!strings.Contains(line, `comment "`+openWrtFirewallCommentPrefix) {
			continue
		}
		current := false
		for comment := range currentComments {
			if strings.Contains(line, `comment "`+comment+`"`) {
				current = true
				break
			}
		}
		if current {
			continue
		}
		match := nftHandlePattern.FindStringSubmatch(strings.TrimSpace(line))
		if len(match) == 2 {
			handles = append(handles, match[1])
		}
	}
	sort.Strings(handles)
	return handles
}

func firewallRuleComments(rules []openWrtFirewallRule) map[string]struct{} {
	result := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		result[rule.comment] = struct{}{}
	}
	return result
}
